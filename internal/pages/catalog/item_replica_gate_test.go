package catalog

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/catalogsync"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// ut-docs#2817 (replacing ut-docs#1689's hard refusal): on a till that
// follows a main till, an item/variant/barcode/modifier save is written
// through to the main till (catalogsync.Forward -> POST
// /api/sync/catalog/apply) instead of being refused, and the main till's
// answer is relayed to the operator. It is NEVER written locally: the admin
// bundle is main-till-wins, so a local write would be reverted by the next
// pull. The main till here is a stub that records what arrives; the real
// main-till endpoint is exercised in internal/pages (sync_catalog_test.go
// and catalog_write_through_test.go).
//
// Assertions use t.Errorf, not t.Fatalf (review finding, ut-docs#1689): a
// regression in one route must not mask the others.

const (
	replicaBearer  = "bearer-replica"
	unreachableMsg = "Can't reach the main till"
	conflictMsg    = "changed on another till"
)

type stubMainCall struct {
	auth string
	in   catalogsync.ApplyRequest
}

// stubMain is a main till that answers every write-through with status,
// recording each call.
type stubMain struct {
	srv    *httptest.Server
	mu     sync.Mutex
	calls  []stubMainCall
	status int
	answer string
}

func newStubMain(t *testing.T) *stubMain {
	t.Helper()
	m := &stubMain{status: http.StatusOK}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != catalogsync.ApplyPath {
			t.Errorf("unexpected main-till call %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		var in catalogsync.ApplyRequest
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Errorf("decode write-through body: %v", err)
		}
		m.mu.Lock()
		m.calls = append(m.calls, stubMainCall{auth: r.Header.Get("Authorization"), in: in})
		status, answer := m.status, m.answer
		m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if answer != "" {
			_, _ = w.Write([]byte(answer))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": catalogsync.ApplyAnswer{
				Status:  http.StatusOK,
				Headers: map[string]string{"Content-Type": "text/html; charset=utf-8", "HX-Trigger": "catalog-saved"},
				Body:    `<div id="from-main">` + in.Path + `</div>`,
			},
			"error": nil,
		})
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *stubMain) last(t *testing.T) stubMainCall {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.calls) == 0 {
		t.Fatal("the main till was never called")
	}
	return m.calls[len(m.calls)-1]
}

func (m *stubMain) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

type replicaCatalog struct {
	db  *sql.DB
	mux *http.ServeMux
}

// newReplicaCatalog is an additional till with the catalogue handlers over
// its own database, following mainURL.
func newReplicaCatalog(t *testing.T, mainURL string) replicaCatalog {
	t.Helper()
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	t.Cleanup(func() { db.Close() })
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "COFFEE", Name: "Flat White", BasePrice: 320, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "var1", ItemID: "itm1", SKU: "COFFEE-L", Name: "Large", Price: 380, IsActive: true})
	testsupport.SeedBarcode(t, db, "5000000000000", "itm1", true)
	testsupport.SeedModifierGroup(t, db, "grp1", "itm1", "Milk", false, 0, 1, 0, true)
	for _, q := range []string{
		`UPDATE items SET updated_at = '2026-01-01 08:00:00' WHERE id = 'itm1'`,
		`UPDATE item_variants SET updated_at = '2026-01-01 08:00:00' WHERE id = 'var1'`,
		`UPDATE item_modifier_groups SET updated_at = '2026-01-01 08:00:00' WHERE id = 'grp1'`,
		// NewCatalogTestDB's schema omits "settings" for handler groups that
		// don't otherwise need it.
		`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	st := settings.NewStore(db)
	if err := st.Set(t.Context(), "sync.primary_url", mainURL); err != nil {
		t.Fatal(err)
	}
	if err := st.Set(t.Context(), "sync.bearer", replicaBearer); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}, Settings: st})
	return replicaCatalog{db: db, mux: mux}
}

func (rc replicaCatalog) post(t *testing.T, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	rc.mux.ServeHTTP(rec, req)
	return rec
}

// localState is every row a refused or written-through save could touch on
// the additional till; it must never change.
func (rc replicaCatalog) localState(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	for _, q := range []string{
		`SELECT id || '|' || name || '|' || base_price || '|' || is_active || '|' || COALESCE(cost_price, '') || '|' || lead_time_days || '|' || reorder_level || '|' || COALESCE(updated_at, '') FROM items ORDER BY id`,
		`SELECT id || '|' || name || '|' || price || '|' || is_active || '|' || updated_at FROM item_variants ORDER BY id`,
		`SELECT barcode || '|' || item_id FROM item_barcodes ORDER BY barcode`,
		`SELECT barcode || '|' || variant_id FROM variant_barcodes ORDER BY barcode`,
		`SELECT id || '|' || name || '|' || updated_at FROM item_modifier_groups ORDER BY id`,
		`SELECT id || '|' || name FROM item_modifier_options ORDER BY id`,
	} {
		rows, err := rc.db.Query(q)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			b.WriteString(s + "\n")
		}
		rows.Close()
	}
	return b.String()
}

// writeThroughCases is every in-scope catalogue mutation on this package's
// routes (ut-docs#2817 scope).
var writeThroughCases = []struct {
	label, path string
	form        url.Values
}{
	{"create item", "/api/catalog/item", url.Values{"name": {"New Item"}, "price": {"100"}}},
	{"update item", "/api/catalog/item/update", url.Values{"id": {"itm1"}, "name": {"Renamed"}, "price": {"999"}}},
	{"deactivate item", "/api/catalog/item/deactivate", url.Values{"id": {"itm1"}}},
	{"set item cost", "/api/catalog/item-cost", url.Values{"panelItem": {"itm1"}, "cost": {"1.50"}}},
	{"set item lead time", "/api/catalog/item-lead-time", url.Values{"panelItem": {"itm1"}, "leadTimeDays": {"5"}}},
	{"set item reorder level", "/api/catalog/item-reorder-level", url.Values{"panelItem": {"itm1"}, "reorderLevel": {"5"}}},
	{"create variant", "/api/catalog/variant", url.Values{"itemId": {"itm1"}, "name": {"Small"}, "price": {"250"}}},
	{"update variant", "/api/catalog/variant", url.Values{"itemId": {"itm1"}, "id": {"var1"}, "name": {"Renamed"}, "price": {"999"}}},
	{"deactivate variant", "/api/catalog/variant/deactivate", url.Values{"id": {"var1"}}},
	{"attach barcode to item", "/api/catalog/barcode", url.Values{"itemId": {"itm1"}, "barcode": {"5000000000099"}}},
	{"attach barcode to variant", "/api/catalog/barcode", url.Values{"variantId": {"var1"}, "barcode": {"5000000000098"}}},
	{"detach barcode", "/api/catalog/barcode/delete", url.Values{"barcode": {"5000000000000"}, "panelItem": {"itm1"}}},
	{"update modifier group", "/api/catalog/modifier-group", url.Values{"id": {"grp1"}, "itemId": {"itm1"}, "name": {"Milks"}}},
	{"create modifier option", "/api/catalog/modifier-option", url.Values{"groupId": {"grp1"}, "itemId": {"itm1"}, "name": {"Oat"}}},
	{"attach modifier group", "/api/catalog/modifier-group/attach", url.Values{"groupId": {"grp1"}, "itemId": {"itm1"}}},
	{"detach modifier group", "/api/catalog/modifier-group/detach", url.Values{"groupId": {"grp1"}, "itemId": {"itm1"}}},
	{"delete modifier group", "/api/catalog/modifier-group/delete", url.Values{"groupId": {"grp1"}}},
}

func TestCatalogItemMutations_WriteThroughOnReplica(t *testing.T) {
	main := newStubMain(t)
	rc := newReplicaCatalog(t, main.srv.URL)
	before := rc.localState(t)

	for _, c := range writeThroughCases {
		n := main.count()
		rec := rc.post(t, c.path, c.form)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `<div id="from-main">`+c.path+`</div>`) {
			t.Errorf("%s on replica: want the main till's answer relayed, got %d %q", c.label, rec.Code, rec.Body.String())
			continue
		}
		if got := rec.Header().Get("HX-Trigger"); got != "catalog-saved" {
			t.Errorf("%s on replica: main till's HX-Trigger not relayed, got %q", c.label, got)
		}
		if main.count() != n+1 {
			t.Errorf("%s on replica: main till calls %d -> %d, want exactly one", c.label, n, main.count())
			continue
		}
		call := main.last(t)
		if call.auth != "Bearer "+replicaBearer {
			t.Errorf("%s: Authorization = %q, want this till's sync bearer", c.label, call.auth)
		}
		if call.in.Method != http.MethodPost || call.in.Path != c.path {
			t.Errorf("%s: forwarded %s %s, want POST %s", c.label, call.in.Method, call.in.Path, c.path)
		}
		for k, v := range c.form {
			if call.in.Form.Get(k) != v[0] {
				t.Errorf("%s: forwarded form %s = %q, want %q", c.label, k, call.in.Form.Get(k), v[0])
			}
		}
		if call.in.ActorID == "" || call.in.Headers["HX-Request"] != "true" || call.in.Locale == "" {
			t.Errorf("%s: forwarded actor/headers/locale missing: %+v", c.label, call.in)
		}
	}
	if after := rc.localState(t); after != before {
		t.Errorf("a written-through save changed the additional till's own rows:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// The optimistic conflict check's base: an edit of an existing record sends
// the updated_at the editor carried, or this till's own copy of it; a
// create, a deactivation and a child row send none.
func TestCatalogItemMutations_WriteThroughSendsConflictBase(t *testing.T) {
	main := newStubMain(t)
	rc := newReplicaCatalog(t, main.srv.URL)

	rc.post(t, "/api/catalog/item/update", url.Values{"id": {"itm1"}, "name": {"Renamed"}, "price": {"999"}})
	if got := main.last(t).in.BaseUpdatedAt; got != "2026-01-01 08:00:00" {
		t.Errorf("item update base = %q, want this till's copy of updated_at", got)
	}
	rc.post(t, "/api/catalog/item/update", url.Values{"id": {"itm1"}, "name": {"Renamed"}, "price": {"999"}, "base_updated_at": {"2026-01-01 07:00:00"}})
	if got := main.last(t).in.BaseUpdatedAt; got != "2026-01-01 07:00:00" {
		t.Errorf("item update base = %q, want the stamp the editor carried", got)
	}
	rc.post(t, "/api/catalog/variant", url.Values{"itemId": {"itm1"}, "id": {"var1"}, "name": {"L"}, "price": {"1"}})
	if got := main.last(t).in.BaseUpdatedAt; got != "2026-01-01 08:00:00" {
		t.Errorf("variant update base = %q", got)
	}
	rc.post(t, "/api/catalog/modifier-group", url.Values{"id": {"grp1"}, "itemId": {"itm1"}, "name": {"M"}})
	if got := main.last(t).in.BaseUpdatedAt; got != "2026-01-01 08:00:00" {
		t.Errorf("modifier group update base = %q", got)
	}
	for _, c := range []struct {
		path string
		form url.Values
	}{
		{"/api/catalog/item", url.Values{"name": {"New"}, "price": {"1"}}},
		{"/api/catalog/item/deactivate", url.Values{"id": {"itm1"}}},
		{"/api/catalog/variant", url.Values{"itemId": {"itm1"}, "name": {"New"}, "price": {"1"}}},
		{"/api/catalog/barcode", url.Values{"itemId": {"itm1"}, "barcode": {"5000000000099"}}},
	} {
		rc.post(t, c.path, c.form)
		if got := main.last(t).in.BaseUpdatedAt; got != "" {
			t.Errorf("%s base = %q, want none", c.path, got)
		}
	}
	// An elevation PIN never travels.
	rc.post(t, "/api/catalog/item/update", url.Values{"id": {"itm1"}, "name": {"R"}, "price": {"1"}, "override_pin": {"7391"}})
	if _, ok := main.last(t).in.Form["override_pin"]; ok {
		t.Error("override_pin was forwarded to the main till")
	}
}

// Main till unreachable: every save is refused with the "change it when the
// main till is reachable" message (502) and nothing changes locally.
func TestCatalogItemMutations_MainUnreachableRefusedWithoutLocalWrite(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	rc := newReplicaCatalog(t, deadURL)
	before := rc.localState(t)
	for _, c := range writeThroughCases {
		rec := rc.post(t, c.path, c.form)
		if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), unreachableMsg) {
			t.Errorf("%s with the main till unreachable: want 502 with %q, got %d %q", c.label, unreachableMsg, rec.Code, rec.Body.String())
		}
	}
	if after := rc.localState(t); after != before {
		t.Errorf("a refused save changed the additional till's rows:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// The main till answered conflict: the operator is told the record changed
// on another till and to reload, and nothing changes locally.
func TestCatalogItemMutations_ConflictShowsReloadMessage(t *testing.T) {
	main := newStubMain(t)
	main.status = http.StatusConflict
	main.answer = `{"data":null,"error":{"code":"conflict","message":"the record changed on the main till"}}`
	rc := newReplicaCatalog(t, main.srv.URL)
	before := rc.localState(t)
	rec := rc.post(t, "/api/catalog/item/update", url.Values{"id": {"itm1"}, "name": {"Renamed"}, "price": {"999"}})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), conflictMsg) {
		t.Fatalf("conflict: want 409 with %q, got %d %q", conflictMsg, rec.Code, rec.Body.String())
	}
	if after := rc.localState(t); after != before {
		t.Errorf("a conflicting save changed the additional till's rows")
	}
	// A main-till permission refusal is a 403, like a local one.
	main.status = http.StatusForbidden
	main.answer = `{"data":null,"error":{"code":"forbidden","message":"actor may not make this change"}}`
	if rec := rc.post(t, "/api/catalog/item/update", url.Values{"id": {"itm1"}, "name": {"R"}, "price": {"1"}}); rec.Code != http.StatusForbidden {
		t.Errorf("forbidden on the main till: want 403, got %d %q", rec.Code, rec.Body.String())
	}
	// A 5xx is "couldn't get an answer", never the main till's decision.
	main.status = http.StatusInternalServerError
	main.answer = `{"data":null,"error":{"code":"conflict","message":"x"}}`
	if rec := rc.post(t, "/api/catalog/item/update", url.Values{"id": {"itm1"}, "name": {"R"}, "price": {"1"}}); rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), unreachableMsg) {
		t.Errorf("5xx from the main till: want 502 unreachable, got %d %q", rec.Code, rec.Body.String())
	}
}

// Routes outside ut-docs#2817's scope keep their main-till-only refusal and
// never reach the main till: the bulk barcode backfill, option sets.
func TestCatalogOutOfScopeMutations_StillRefusedOnReplica(t *testing.T) {
	main := newStubMain(t)
	rc := newReplicaCatalog(t, main.srv.URL)
	const wantMsg = "manage the catalog on the primary till"
	for _, c := range []struct {
		label, path string
		form        url.Values
	}{
		{"barcode backfill commit", "/api/catalog/barcode-backfill", url.Values{}},
		{"option set", "/api/catalog/option-set", url.Values{"name": {"Size"}}},
	} {
		rec := rc.post(t, c.path, c.form)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), wantMsg) {
			t.Errorf("%s on replica: want 409 with %q, got %d %q", c.label, wantMsg, rec.Code, rec.Body.String())
		}
	}
	if main.count() != 0 {
		t.Errorf("an out-of-scope route reached the main till (%d calls)", main.count())
	}
	// The GET preview keeps working on a replica — a read-only dry run.
	getReq := httptest.NewRequest(http.MethodGet, "/api/catalog/barcode-backfill", nil)
	getRec := httptest.NewRecorder()
	rc.mux.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Errorf("GET barcode-backfill preview on replica: want 200, got %d: %s", getRec.Code, getRec.Body.String())
	}
}

// Every route Forward is wired into resolves to a write-through route: a
// call site missing from catalogsync.Routes would be refused at run time.
func TestCatalogWriteThroughCases_AllResolve(t *testing.T) {
	for _, c := range writeThroughCases {
		if _, _, ok := catalogsync.Resolve(http.MethodPost, c.path, c.form); !ok {
			t.Errorf("%s (%s) is not in catalogsync.Routes", c.label, c.path)
		}
	}
}
