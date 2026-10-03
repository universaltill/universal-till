package pages

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/catalog"
	"github.com/universaltill/universal-till/internal/pages/catalogsync"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
	"github.com/universaltill/universal-till/internal/ui"
)

// Main-till side of ut-docs#2817: POST /api/sync/catalog/apply is where an
// additional till's catalogue save lands (catalogsync.Forward sends it).
// Same shape as sync_settings_test.go: syncTill bearer, JSON envelope, a
// real migrated database; the actor's permission is decided with the MAIN
// till's own users and role table, and the save runs the main till's own
// catalogue handler.

const syncCatalogBearer = "bearer-cat-t2"

type syncCatalogMain struct {
	mux *http.ServeMux
	dp  *common.Deps
}

// newSyncCatalogMain is a main till with every catalogue screen's handlers
// and the write-through endpoint on one mux, as pages.Init mounts them.
func newSyncCatalogMain(t *testing.T) *syncCatalogMain {
	t.Helper()
	m := newCatalogTill(t)
	registerSyncCatalog(m.mux, m.dp)
	return m
}

// newCatalogTill is one till with every catalogue screen's handlers over
// its own migrated database: the catalogue rows, the users (with PIN 7391)
// and "Till 2"'s bearer, as the admin bundle leaves both tills.
func newCatalogTill(t *testing.T) *syncCatalogMain {
	t.Helper()
	t.Setenv("UT_AUTH", "on")
	chdirRoot(t)
	initPagesI18n(t)
	dbase := openPagesTestDB(t)
	t.Cleanup(func() { dbase.Close() })
	dp := &common.Deps{
		Db:       dbase,
		Settings: settings.NewStore(dbase),
		AuthSvc:  auth.NewService(dbase),
		BtnStore: ui.NewButtonStore(dbase),
		Cfg:      &config.Config{Theme: "default"},
		State:    common.RuntimeState{Theme: "default", BrowsingMode: common.BrowsingModeStripOverflow},
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
	}
	seedSyncOrdersTill(t, dp, "Till 2", syncCatalogBearer)
	pin, err := auth.HashPIN("7391")
	if err != nil {
		t.Fatal(err)
	}
	insertTestUserWithPIN(t, dbase, "m-1", "mia", "Mia", "manager", pin)
	insertTestUserWithPIN(t, dbase, "c-1", "cara", "Cara", "cashier", pin)
	seedSyncCatalogRows(t, dbase)
	mux := http.NewServeMux()
	catalog.Register(mux, dp)
	registerCategories(mux, dp)
	registerButtonsAPI(mux, dp)
	registerDesignerCategoriesAPI(mux, dp)
	return &syncCatalogMain{mux: mux, dp: dp}
}

// seedSyncCatalogRows is the catalogue both tills start from (the admin
// bundle would have delivered the same rows, stamps included).
func seedSyncCatalogRows(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, q := range []string{
		`INSERT INTO categories (id, name, updated_at) VALUES ('cat1', 'Drinks', '2026-01-01 08:00:00')`,
		`INSERT INTO items (id, sku, name, base_price, unit, is_active, category_id, updated_at) VALUES ('itm1', 'SKU1', 'Flat White', 320, 'pcs', 1, 'cat1', '2026-01-01 08:00:00')`,
		`INSERT INTO item_variants (id, item_id, sku, name, price, is_active, updated_at) VALUES ('var1', 'itm1', 'SKU1-L', 'Large', 380, 1, '2026-01-01 08:00:00')`,
		`INSERT INTO shortcut_buttons (barcode, label, item_id, sort_order, updated_at) VALUES ('SKU1', 'Flat White', 'itm1', 0, '2026-01-01 08:00:00')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

func postSyncCatalogApply(mux *http.ServeMux, in catalogsync.ApplyRequest, bearer string) *httptest.ResponseRecorder {
	b, _ := json.Marshal(in)
	req := httptest.NewRequest(http.MethodPost, catalogsync.ApplyPath, strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

type syncCatalogApplyResponse struct {
	Data  *catalogsync.ApplyAnswer `json:"data"`
	Error *catalogsync.WireError   `json:"error"`
}

func decodeSyncCatalog(t *testing.T, rec *httptest.ResponseRecorder) syncCatalogApplyResponse {
	t.Helper()
	var out syncCatalogApplyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (status %d body %q)", err, rec.Code, rec.Body.String())
	}
	return out
}

func wantSyncCatalogError(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d (body %q)", rec.Code, status, rec.Body.String())
	}
	out := decodeSyncCatalog(t, rec)
	if out.Data != nil || out.Error == nil || out.Error.Code != code {
		t.Fatalf("body = %q, want data null and error.code %q", rec.Body.String(), code)
	}
}

// itemUpdateForm is the item editor's save of itm1 (price in minor units,
// as the editor's submit handler converts it).
func itemUpdateForm(name, priceMinor string) url.Values {
	return url.Values{"id": {"itm1"}, "name": {name}, "price": {priceMinor}, "sku": {"SKU1"}, "unit": {"pcs"}, "categoryId": {"cat1"}}
}

func syncCatalogItem(t *testing.T, dp *common.Deps, id string) (name string, price int64, updatedAt string) {
	t.Helper()
	if err := dp.Db.QueryRow(`SELECT name, base_price, COALESCE(updated_at, '') FROM items WHERE id = ?`, id).Scan(&name, &price, &updatedAt); err != nil {
		t.Fatalf("read item %s: %v", id, err)
	}
	return name, price, updatedAt
}

func setItemStamp(t *testing.T, dp *common.Deps, id, stamp string) {
	t.Helper()
	if _, err := dp.Db.Exec(`UPDATE items SET updated_at = ? WHERE id = ?`, stamp, id); err != nil {
		t.Fatal(err)
	}
}

// assertCatalogSyncAudit finds the main till's provenance row for a
// write-through: catalog_changed_via_till by actor, via till-sync + the
// calling till's name + the entity.
func assertCatalogSyncAudit(t *testing.T, dp *common.Deps, actorID, entity, entityID, till string) {
	t.Helper()
	entries, err := data.NewPOSRepo(dp.Db).ListAudit(context.Background(), data.AuditFilters{ActorID: actorID})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	for _, e := range entries {
		if e.Action != "catalog_changed_via_till" || e.EntityID != entityID {
			continue
		}
		if e.EntityType != "catalog" {
			t.Fatalf("audit entity type = %q, want catalog", e.EntityType)
		}
		p := e.DataJSON
		if !strings.Contains(p, "till-sync") || !strings.Contains(p, till) || !strings.Contains(p, `"entity":"`+entity+`"`) {
			t.Fatalf("audit payload = %s, want via till-sync, till %q, entity %q", p, till, entity)
		}
		return
	}
	t.Fatalf("no catalog_changed_via_till audit by %s on %s, got %+v", actorID, entityID, entries)
}

func countCatalogSyncAudits(t *testing.T, dp *common.Deps) int {
	t.Helper()
	var n int
	if err := dp.Db.QueryRow(`SELECT count(*) FROM audit_log WHERE action = 'catalog_changed_via_till'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSyncCatalogApply_ItemSaveRunsMainHandlerAuditsAndAnswers(t *testing.T) {
	m := newSyncCatalogMain(t)
	setItemStamp(t, m.dp, "itm1", "2026-01-01 10:00:00")
	rec := postSyncCatalogApply(m.mux, catalogsync.ApplyRequest{
		Method: http.MethodPost, Path: "/api/catalog/item/update",
		Form:          itemUpdateForm("Oat Flat White", "395"),
		BaseUpdatedAt: "2026-01-01 10:00:00",
		ActorID:       "m-1",
		Headers:       map[string]string{"HX-Request": "true"},
		Locale:        "en",
	}, syncCatalogBearer)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %q", rec.Code, rec.Body.String())
	}
	out := decodeSyncCatalog(t, rec)
	if out.Error != nil || out.Data == nil {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if out.Data.Status != http.StatusOK {
		t.Fatalf("relayed handler status = %d body %q", out.Data.Status, out.Data.Body)
	}
	// The main till's own handler answered: its row OOB fragment.
	if !strings.Contains(out.Data.Body, "catalog-row-itm1") || !strings.Contains(out.Data.Body, "Oat Flat White") {
		t.Fatalf("relayed body is not the catalogue row fragment: %q", out.Data.Body)
	}
	name, price, stamp := syncCatalogItem(t, m.dp, "itm1")
	if name != "Oat Flat White" || price != 395 {
		t.Fatalf("main till item = %q %d, want the change applied", name, price)
	}
	if stamp == "2026-01-01 10:00:00" || out.Data.UpdatedAt != stamp || out.Data.Entity != data.CatalogKindItem || out.Data.EntityID != "itm1" {
		t.Fatalf("updated_at = %q, answer = %+v — the save must advance the stamp and name it", stamp, out.Data)
	}
	assertCatalogSyncAudit(t, m.dp, "m-1", data.CatalogKindItem, "itm1", "Till 2")
}

func TestSyncCatalogApply_RequiresBearer(t *testing.T) {
	m := newSyncCatalogMain(t)
	in := catalogsync.ApplyRequest{Method: http.MethodPost, Path: "/api/catalog/item/update", Form: itemUpdateForm("X", "1"), ActorID: "m-1"}
	for _, bearer := range []string{"", "wrong"} {
		wantSyncCatalogError(t, postSyncCatalogApply(m.mux, in, bearer), http.StatusUnauthorized, "unauthorized")
	}
	if name, _, _ := syncCatalogItem(t, m.dp, "itm1"); name != "Flat White" {
		t.Fatalf("a refused call wrote: name = %q", name)
	}
}

func TestSyncCatalogApply_RefusedWhenThisTillFollowsAMain(t *testing.T) {
	m := newSyncCatalogMain(t)
	setReplicaSettings(t, m.dp.Settings, "http://192.0.2.1:8080", "b-123")
	in := catalogsync.ApplyRequest{Method: http.MethodPost, Path: "/api/catalog/item/update", Form: itemUpdateForm("X", "1"), ActorID: "m-1"}
	wantSyncCatalogError(t, postSyncCatalogApply(m.mux, in, syncCatalogBearer), http.StatusConflict, "replica")
	if name, _, _ := syncCatalogItem(t, m.dp, "itm1"); name != "Flat White" {
		t.Fatalf("a refused call wrote: name = %q", name)
	}
}

// The permission is decided on the MAIN till: a cashier there is refused
// even if the additional till let the request through, and nothing is
// written.
func TestSyncCatalogApply_ForbiddenForActorWithoutCatalogManagement(t *testing.T) {
	m := newSyncCatalogMain(t)
	in := catalogsync.ApplyRequest{Method: http.MethodPost, Path: "/api/catalog/item/update", Form: itemUpdateForm("Cashier rename", "1"), ActorID: "c-1"}
	wantSyncCatalogError(t, postSyncCatalogApply(m.mux, in, syncCatalogBearer), http.StatusForbidden, "forbidden")
	if name, _, _ := syncCatalogItem(t, m.dp, "itm1"); name != "Flat White" {
		t.Fatalf("a forbidden change wrote: name = %q", name)
	}
	// A manager's PIN approval (decided on the additional till) is
	// re-checked here: the approver's role holds catalog_management.
	in.ApproverID = "m-1"
	if rec := postSyncCatalogApply(m.mux, in, syncCatalogBearer); rec.Code != http.StatusOK {
		t.Fatalf("approved change = %d %q", rec.Code, rec.Body.String())
	}
	if name, _, _ := syncCatalogItem(t, m.dp, "itm1"); name != "Cashier rename" {
		t.Fatalf("approved change not applied: name = %q", name)
	}
	// An approver who does not hold the permission is refused.
	in.ApproverID = "c-1"
	wantSyncCatalogError(t, postSyncCatalogApply(m.mux, in, syncCatalogBearer), http.StatusForbidden, "forbidden")
}

func TestSyncCatalogApply_UnknownActorRefused(t *testing.T) {
	m := newSyncCatalogMain(t)
	for _, actor := range []string{"", "ghost"} {
		in := catalogsync.ApplyRequest{Method: http.MethodPost, Path: "/api/catalog/item/update", Form: itemUpdateForm("X", "1"), ActorID: actor}
		wantSyncCatalogError(t, postSyncCatalogApply(m.mux, in, syncCatalogBearer), http.StatusBadRequest, "unknown_actor")
	}
}

// The optimistic conflict check: the item changed on the main till since
// the additional till's editor loaded it -> 409 conflict, nothing written,
// no audit.
func TestSyncCatalogApply_ConflictWhenRecordChangedOnMain(t *testing.T) {
	m := newSyncCatalogMain(t)
	setItemStamp(t, m.dp, "itm1", "2026-01-01 10:05:00")
	in := catalogsync.ApplyRequest{
		Method: http.MethodPost, Path: "/api/catalog/item/update",
		Form:          itemUpdateForm("Stale edit", "100"),
		BaseUpdatedAt: "2026-01-01 10:00:00",
		ActorID:       "m-1",
	}
	wantSyncCatalogError(t, postSyncCatalogApply(m.mux, in, syncCatalogBearer), http.StatusConflict, catalogsync.CodeConflict)
	name, price, stamp := syncCatalogItem(t, m.dp, "itm1")
	if name != "Flat White" || price != 320 || stamp != "2026-01-01 10:05:00" {
		t.Fatalf("conflicting save touched the row: %q %d %q", name, price, stamp)
	}
	if n := countCatalogSyncAudits(t, m.dp); n != 0 {
		t.Fatalf("a refused save was audited (%d rows)", n)
	}
	// The same save on a fresh base goes through.
	in.BaseUpdatedAt = "2026-01-01 10:05:00"
	if rec := postSyncCatalogApply(m.mux, in, syncCatalogBearer); rec.Code != http.StatusOK {
		t.Fatalf("fresh-base save = %d %q", rec.Code, rec.Body.String())
	}
	if name, _, _ := syncCatalogItem(t, m.dp, "itm1"); name != "Stale edit" {
		t.Fatalf("fresh-base save not applied: %q", name)
	}
}

// Routes outside the write-through (option sets, settings, anything else)
// are never dispatched from here.
func TestSyncCatalogApply_RouteOutsideTheWriteThroughRefused(t *testing.T) {
	m := newSyncCatalogMain(t)
	for _, path := range []string{"/api/catalog/option-set", "/api/settings/upsert", "/api/sync/catalog/apply", "/api/catalog/barcode-backfill"} {
		in := catalogsync.ApplyRequest{Method: http.MethodPost, Path: path, Form: url.Values{"name": {"x"}}, ActorID: "m-1"}
		wantSyncCatalogError(t, postSyncCatalogApply(m.mux, in, syncCatalogBearer), http.StatusBadRequest, catalogsync.CodeNotSupported)
	}
	in := catalogsync.ApplyRequest{Method: http.MethodGet, Path: "/api/catalog/item/update", Form: itemUpdateForm("X", "1"), ActorID: "m-1"}
	wantSyncCatalogError(t, postSyncCatalogApply(m.mux, in, syncCatalogBearer), http.StatusBadRequest, catalogsync.CodeNotSupported)
}

// A path ServeMux would clean ("." or ".." segments, "//") never reaches the
// mux: ServeMux answers its own 301 to the cleaned path before any handler
// runs, and a 3xx would otherwise be relayed to the operator and audited
// here as a successful save (ut-docs#2817 review finding).
func TestSyncCatalogApply_UncleanPathRefusedNotAudited(t *testing.T) {
	m := newSyncCatalogMain(t)
	for _, p := range []string{"/api/categories/..", "/api/categories/.", "/api/categories/../reorder", "/api//categories/cat1"} {
		in := catalogsync.ApplyRequest{Method: http.MethodPost, Path: p, Form: url.Values{"name": {"x"}}, ActorID: "m-1"}
		wantSyncCatalogError(t, postSyncCatalogApply(m.mux, in, syncCatalogBearer), http.StatusBadRequest, "invalid_body")
	}
	if n := countCatalogSyncAudits(t, m.dp); n != 0 {
		t.Fatalf("a refused path was audited (%d rows)", n)
	}
}

// A save the main till's handler itself refuses (validation) is relayed as
// the handler answered it — the operator sees the same message a local save
// shows — and is neither audited as a write-through nor nudged.
func TestSyncCatalogApply_HandlerRefusalRelayedNotAudited(t *testing.T) {
	m := newSyncCatalogMain(t)
	in := catalogsync.ApplyRequest{Method: http.MethodPost, Path: "/api/catalog/item/update", Form: url.Values{"id": {"itm1"}, "name": {""}}, ActorID: "m-1"}
	rec := postSyncCatalogApply(m.mux, in, syncCatalogBearer)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %q", rec.Code, rec.Body.String())
	}
	out := decodeSyncCatalog(t, rec)
	if out.Data == nil || out.Data.Status != http.StatusBadRequest {
		t.Fatalf("answer = %+v, want the handler's own 400 relayed", out.Data)
	}
	if n := countCatalogSyncAudits(t, m.dp); n != 0 {
		t.Fatalf("a refused save was audited (%d rows)", n)
	}
}

// A photo upload never travels (the asset ledger distributes photos).
func TestSyncCatalogApply_PhotoFieldRefused(t *testing.T) {
	m := newSyncCatalogMain(t)
	in := catalogsync.ApplyRequest{Method: http.MethodPost, Path: "/api/categories/cat1", Form: url.Values{"name": {"Drinks"}, "image": {"upload"}}, ActorID: "m-1"}
	wantSyncCatalogError(t, postSyncCatalogApply(m.mux, in, syncCatalogBearer), http.StatusBadRequest, catalogsync.CodePhoto)
}

// Categories travel too, and the conflict check applies to them.
func TestSyncCatalogApply_CategorySaveAndConflict(t *testing.T) {
	m := newSyncCatalogMain(t)
	if _, err := m.dp.Db.Exec(`UPDATE categories SET updated_at = '2026-01-01 09:00:00' WHERE id = 'cat1'`); err != nil {
		t.Fatal(err)
	}
	in := catalogsync.ApplyRequest{
		Method: http.MethodPost, Path: "/api/categories/cat1",
		Form:          url.Values{"name": {"Hot drinks"}},
		BaseUpdatedAt: "2026-01-01 08:00:00",
		ActorID:       "m-1",
		Headers:       map[string]string{"HX-Request": "true"},
	}
	wantSyncCatalogError(t, postSyncCatalogApply(m.mux, in, syncCatalogBearer), http.StatusConflict, catalogsync.CodeConflict)
	in.BaseUpdatedAt = "2026-01-01 09:00:00"
	rec := postSyncCatalogApply(m.mux, in, syncCatalogBearer)
	out := decodeSyncCatalog(t, rec)
	if rec.Code != http.StatusOK || out.Data == nil || out.Data.Headers["HX-Redirect"] != "/categories" {
		t.Fatalf("category save = %d %+v, want the handler's HX-Redirect relayed", rec.Code, out.Data)
	}
	var name string
	if err := m.dp.Db.QueryRow(`SELECT name FROM categories WHERE id = 'cat1'`).Scan(&name); err != nil || name != "Hot drinks" {
		t.Fatalf("category name = %q (%v)", name, err)
	}
	assertCatalogSyncAudit(t, m.dp, "m-1", data.CatalogKindCategory, "cat1", "Till 2")
}

// The endpoint is mounted with a literal pattern (the demo route scan reads
// literals); it must stay the path catalogsync.Forward calls.
func TestSyncCatalogApply_MountedAtTheForwardPath(t *testing.T) {
	if catalogsync.ApplyPath != "/api/sync/catalog/apply" {
		t.Fatalf("catalogsync.ApplyPath = %q, but registerSyncCatalog mounts /api/sync/catalog/apply", catalogsync.ApplyPath)
	}
}

// ut-docs#3606: /api/buttons/add re-labels the item's EXISTING button row
// (ShortcutsRepo.AddButton) when one exists under a different code, so the
// conflict check must look at that row — not at the posted code, which
// names no row at all ("not found" skipped the check entirely).
func TestSyncCatalogApply_ButtonAddConflictCheckedOnTheRelabelledRow(t *testing.T) {
	m := newSyncCatalogMain(t)
	if _, err := m.dp.Db.Exec(`UPDATE shortcut_buttons SET updated_at = '2026-01-01 09:00:00' WHERE barcode = 'SKU1'`); err != nil {
		t.Fatal(err)
	}
	in := catalogsync.ApplyRequest{
		Method: http.MethodPost, Path: "/api/buttons/add",
		Form:          url.Values{"label": {"Flat White XL"}, "code": {"NEWCODE"}, "itemId": {"itm1"}},
		BaseUpdatedAt: "2026-01-01 08:00:00",
		ActorID:       "m-1",
	}
	wantSyncCatalogError(t, postSyncCatalogApply(m.mux, in, syncCatalogBearer), http.StatusConflict, catalogsync.CodeConflict)
	var label string
	if err := m.dp.Db.QueryRow(`SELECT label FROM shortcut_buttons WHERE barcode = 'SKU1'`).Scan(&label); err != nil || label != "Flat White" {
		t.Fatalf("conflicting add re-labelled the row: %q (%v)", label, err)
	}
	in.BaseUpdatedAt = "2026-01-01 09:00:00"
	rec := postSyncCatalogApply(m.mux, in, syncCatalogBearer)
	out := decodeSyncCatalog(t, rec)
	if rec.Code != http.StatusOK || out.Data == nil || out.Data.Status >= 300 {
		t.Fatalf("fresh-base add = %d %q", rec.Code, rec.Body.String())
	}
	var stamp string
	if err := m.dp.Db.QueryRow(`SELECT label, updated_at FROM shortcut_buttons WHERE barcode = 'SKU1'`).Scan(&label, &stamp); err != nil || label != "Flat White XL" {
		t.Fatalf("add did not re-label the existing row: %q (%v)", label, err)
	}
	if out.Data.Entity != data.CatalogKindItemButton || out.Data.EntityID != "itm1" || out.Data.UpdatedAt != stamp {
		t.Fatalf("answer = %+v, want the re-labelled row (item itm1, stamp %q)", out.Data, stamp)
	}
}
