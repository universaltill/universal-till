package pages

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
)

func newRegistersTestMux(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)

	// Settings backs the replica check (SyncPrimaryURL) — left unset, this
	// till is a primary/standalone, matching every pre-existing test in
	// this file. ut-docs#1590's replica tests set sync.primary_url on the
	// same store (same convention as journal_page_test.go).
	d := &common.Deps{Db: db, Menu: []common.MenuItem{{Href: "/", Label: "Home"}}, AuthSvc: auth.NewService(db), Settings: settings.NewStore(db)}
	mux := http.NewServeMux()
	registerRegisters(mux, d)
	return mux, d
}

// ut-docs#901: GET /registers must be reachable under UT_AUTH=off with no
// session — same fix and rationale as locations_page_test.go's
// TestLocationsPage_ReachableUnderAuthOff.
func TestRegistersPage_ReachableUnderAuthOff(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newRegistersTestMux(t)

	req := httptest.NewRequest(http.MethodGet, "/registers", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /registers under UT_AUTH=off = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

// Mutating handlers, not just the GET page, must also pick up canPerform's
// UT_AUTH=off bypass -- independent review finding, ut-docs#901 (the
// original regression test above only pinned the read path).
func TestRegistersPageCreate_ReachableUnderAuthOff(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newRegistersTestMux(t)

	rec := postForm(mux, "/api/registers", url.Values{"name": {"Auth-Off Till"}}, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/registers" {
		t.Fatalf("create under UT_AUTH=off: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
}

// ut-docs#1590: a satellite-created register has no shift/sale history yet
// to FK-block ApplyAdmin's deleteMissing, so a manager creating one directly
// on a replica would have it silently wiped on the very next admin pull once
// registers/stock_locations sync (see sync_admin_repo.go's adminTables). All
// three mutating actions (create/rename/deactivate) must refuse on a
// replica with a clear, localized error — never a silent accept.
func TestRegistersPage_MutationsRefusedOnReplica(t *testing.T) {
	mux, d := newRegistersTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	// A register that existed before this till became a replica (e.g.
	// synced down from the primary, or created back when this till was
	// still standalone) — used below to check rename/deactivate, since
	// create itself is refused and can't produce one.
	const regID = "reg-existing"
	if _, err := d.Db.Exec(`INSERT INTO registers (id, name, is_active) VALUES (?, 'Existing Till', 1)`, regID); err != nil {
		t.Fatalf("seed register: %v", err)
	}

	if err := d.Settings.Set(t.Context(), "sync.primary_url", "http://primary.example"); err != nil {
		t.Fatalf("set primary_url: %v", err)
	}

	rec := postForm(mux, "/api/registers", url.Values{"name": {"Satellite Till"}}, &manager)
	if rec.Header().Get("Location") != "/registers?err=registers.error.replica_use_primary" {
		t.Fatalf("create on replica: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
	var count int
	if err := d.Db.QueryRow(`SELECT count(*) FROM registers WHERE name = 'Satellite Till'`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("register must not be created on a replica, found %d rows", count)
	}

	// The pre-existing register must also refuse rename and deactivate —
	// an existing row can be silently reverted by the next admin pull just
	// as easily as a new one can be deleted by it.
	rec = postForm(mux, "/api/registers/"+regID, url.Values{"name": {"Renamed"}}, &manager)
	if rec.Header().Get("Location") != "/registers?err=registers.error.replica_use_primary" {
		t.Fatalf("rename on replica: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
	rec = postForm(mux, "/api/registers/"+regID+"/active", url.Values{"active": {"0"}}, &manager)
	if rec.Header().Get("Location") != "/registers?err=registers.error.replica_use_primary" {
		t.Fatalf("deactivate on replica: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestRegistersPagePermissions(t *testing.T) {
	mux, _ := newRegistersTestMux(t)
	cashier := auth.User{ID: "c1", Role: "cashier", DisplayName: "Cash"}

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/registers", nil), cashier)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cashier GET /registers = %d, want 403", rec.Code)
	}
	// ut-docs#1458: GET /registers must render the full layout on 403
	// too, not a bare rail-less body (same fix class as #1455).
	if body := rec.Body.String(); !strings.Contains(body, `class="nav"`) {
		t.Fatalf("cashier's 403 on GET /registers has no nav rail:\n%s", body)
	}

	if rec := postForm(mux, "/api/registers", url.Values{"name": {"Front Till"}}, &cashier); rec.Code != http.StatusForbidden {
		t.Fatalf("cashier create = %d, want 403", rec.Code)
	}
}

// ut-docs#896: the page must warn a manager in-page that creating a second
// register, or deactivating one another till might be bound to, can strand
// that till's register binding -- and point them at the fix.
func TestRegistersPage_ShowsStrandWarning(t *testing.T) {
	mux, _ := newRegistersTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/registers", nil), manager)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /registers = %d", rec.Code)
	}
	if !strings.Contains(body, "register assignment unclear") {
		t.Fatalf("registers page missing the strand warning, got: %s", body)
	}
	// nav.html's own automatic "?" already points every page at a topic via
	// manual.HelpHref, so a bare data-testid="help-hint" check passes
	// regardless of this change. Assert on the explicit multitill link this
	// page's own h1 now carries: two occurrences of the topic's href -- the
	// nav's auto link plus the one added here -- proves the new helpLink is
	// actually present, not just the pre-existing nav one.
	if n := strings.Count(body, `href="/help/multitill"`); n != 2 {
		t.Fatalf("registers page: want 2 links to the multitill help topic (nav auto-link + explicit helpLink), got %d in: %s", n, body)
	}
}

func TestRegistersPageCreate_WhitespaceOnlyNameRejected(t *testing.T) {
	mux, _ := newRegistersTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	rec := postForm(mux, "/api/registers", url.Values{"name": {"   "}}, &manager)
	if rec.Header().Get("Location") != "/registers?err=registers.error.required" {
		t.Fatalf("whitespace-only name: loc=%q", rec.Header().Get("Location"))
	}
}

func TestRegistersPageCreateRenameDeactivate(t *testing.T) {
	mux, d := newRegistersTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	// Create (no location picked).
	rec := postForm(mux, "/api/registers", url.Values{"name": {"Front Till"}}, &manager)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/registers" {
		t.Fatalf("create: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}

	// It shows up on the rendered page.
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/registers", nil), manager)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Front Till") {
		t.Fatalf("registers page missing new register: code=%d body=%s", rec.Code, rec.Body.String())
	}

	// Duplicate name is rejected.
	rec = postForm(mux, "/api/registers", url.Values{"name": {"Front Till"}}, &manager)
	if rec.Header().Get("Location") != "/registers?err=registers.error.create" {
		t.Fatalf("duplicate create: loc=%q", rec.Header().Get("Location"))
	}

	// Create with a location picked.
	rec = postForm(mux, "/api/registers", url.Values{"name": {"Back Till"}, "location_id": {"loc_main"}}, &manager)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create with location: code=%d", rec.Code)
	}
	var gotLocationID string
	if err := d.Db.QueryRow(`SELECT location_id FROM registers WHERE name = 'Back Till'`).Scan(&gotLocationID); err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if gotLocationID != "loc_main" {
		t.Fatalf("location_id = %q, want loc_main", gotLocationID)
	}

	// The rendered page resolves the location id to its display name, and
	// an unassigned register never renders a raw "<nil>" pointer value.
	req = auth.WithUser(httptest.NewRequest(http.MethodGet, "/registers", nil), manager)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, "Main") {
		t.Fatalf("expected the assigned register to show its location name, got: %s", body)
	}
	if strings.Contains(body, "&lt;nil&gt;") {
		t.Fatalf("unassigned register must not render a raw nil pointer, got: %s", body)
	}

	// Find the id we just created (the first, unassigned one).
	var newID string
	if err := d.Db.QueryRow(`SELECT id FROM registers WHERE name = 'Front Till'`).Scan(&newID); err != nil {
		t.Fatalf("lookup: %v", err)
	}

	// Rename.
	rec = postForm(mux, "/api/registers/"+newID, url.Values{"name": {"Front Till Renamed"}}, &manager)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("rename: code=%d", rec.Code)
	}
	var name string
	if err := d.Db.QueryRow(`SELECT name FROM registers WHERE id = ?`, newID).Scan(&name); err != nil || name != "Front Till Renamed" {
		t.Fatalf("rename did not take effect: name=%q err=%v", name, err)
	}

	// Deactivating is fine even with two active registers present.
	rec = postForm(mux, "/api/registers/"+newID+"/active", url.Values{"active": {"0"}}, &manager)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/registers" {
		t.Fatalf("deactivate: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
	var active int
	if err := d.Db.QueryRow(`SELECT is_active FROM registers WHERE id = ?`, newID).Scan(&active); err != nil || active != 0 {
		t.Fatalf("not deactivated: active=%d err=%v", active, err)
	}
}

// ut-docs#895: a register's stock location was previously fixed at creation
// time -- a mis-assignment had no fix short of recreating the register.
func TestRegistersPage_ChangeLocationAfterCreation(t *testing.T) {
	mux, d := newRegistersTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	rec := postForm(mux, "/api/registers", url.Values{"name": {"Front Till"}, "location_id": {"loc_main"}}, &manager)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create: code=%d", rec.Code)
	}
	var id string
	if err := d.Db.QueryRow(`SELECT id FROM registers WHERE name = 'Front Till'`).Scan(&id); err != nil {
		t.Fatalf("lookup: %v", err)
	}
	// Move it to a different location -- loc_back is already seeded by
	// 001_init.sql now that openPagesTestDB runs real migrations (its
	// stock_locations.id is a PRIMARY KEY, so re-inserting it here failed);
	// reuse the migration's own row instead of creating a second one.
	rec = postForm(mux, "/api/registers/"+id, url.Values{"name": {"Front Till"}, "location_id": {"loc_back"}}, &manager)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/registers" {
		t.Fatalf("change location: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
	var gotLocationID string
	if err := d.Db.QueryRow(`SELECT location_id FROM registers WHERE id = ?`, id).Scan(&gotLocationID); err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if gotLocationID != "loc_back" {
		t.Fatalf("location_id = %q, want loc_back", gotLocationID)
	}

	// Clear it back to unassigned ("None").
	rec = postForm(mux, "/api/registers/"+id, url.Values{"name": {"Front Till"}, "location_id": {""}}, &manager)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("clear location: code=%d", rec.Code)
	}
	var nullable sql.NullString
	if err := d.Db.QueryRow(`SELECT location_id FROM registers WHERE id = ?`, id).Scan(&nullable); err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if nullable.Valid {
		t.Fatalf("location_id = %q, want NULL", nullable.String)
	}

	// Existing shift/sale history tied to the register is unaffected by a
	// location change: the register row itself (id, name) is untouched
	// beyond location_id.
	var name string
	if err := d.Db.QueryRow(`SELECT name FROM registers WHERE id = ?`, id).Scan(&name); err != nil || name != "Front Till" {
		t.Fatalf("register identity changed unexpectedly: name=%q err=%v", name, err)
	}
}

// Unlike locations, a register with shift/sale history must still be
// deactivatable -- retiring a till keeps its history, this page never
// consults RegisterInUse as a deactivation blocker.
func TestRegistersPage_DeactivateWithHistoryIsAllowed(t *testing.T) {
	mux, d := newRegistersTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	rec := postForm(mux, "/api/registers", url.Values{"name": {"History Till"}}, &manager)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create: code=%d", rec.Code)
	}
	rec = postForm(mux, "/api/registers", url.Values{"name": {"Other Till"}}, &manager)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create other: code=%d", rec.Code)
	}
	var historyID string
	if err := d.Db.QueryRow(`SELECT id FROM registers WHERE name = 'History Till'`).Scan(&historyID); err != nil {
		t.Fatalf("lookup: %v", err)
	}

	if _, err := d.Db.Exec(`INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, register_id, created_at)
		VALUES ('sale-651', 'R-651', 'completed', 'sale', 'GBP', 0, 0, 0, 0, ?, datetime('now'))`, historyID); err != nil {
		t.Fatalf("seed sale: %v", err)
	}

	rec = postForm(mux, "/api/registers/"+historyID+"/active", url.Values{"active": {"0"}}, &manager)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/registers" {
		t.Fatalf("deactivate with history: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
	var active int
	if err := d.Db.QueryRow(`SELECT is_active FROM registers WHERE id = ?`, historyID).Scan(&active); err != nil || active != 0 {
		t.Fatalf("must be deactivated despite history: active=%d err=%v", active, err)
	}
}

// ut-docs#897: RegisterInUse (added alongside #651) is informational only --
// it must never block deactivation (covered above) -- but it should still
// be surfaced to the manager as a hint before they act.
func TestRegistersPage_ShowsInUseHintForRegisterWithHistory(t *testing.T) {
	mux, d := newRegistersTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	rec := postForm(mux, "/api/registers", url.Values{"name": {"Busy Till"}}, &manager)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create busy: code=%d", rec.Code)
	}
	rec = postForm(mux, "/api/registers", url.Values{"name": {"Idle Till"}}, &manager)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create idle: code=%d", rec.Code)
	}
	var busyID string
	if err := d.Db.QueryRow(`SELECT id FROM registers WHERE name = 'Busy Till'`).Scan(&busyID); err != nil {
		t.Fatalf("lookup: %v", err)
	}

	if _, err := d.Db.Exec(`INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, register_id, created_at)
		VALUES ('sale-897', 'R-897', 'completed', 'sale', 'GBP', 0, 0, 0, 0, ?, datetime('now'))`, busyID); err != nil {
		t.Fatalf("seed sale: %v", err)
	}

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/registers", nil), manager)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /registers = %d", rec.Code)
	}
	body := rec.Body.String()

	// Split the body around each register's own row so the hint's presence
	// is checked per-row, not just "somewhere in the page" -- otherwise a
	// hint rendered for the wrong register would still pass a bare
	// strings.Contains(body, hint) check.
	busyIdx := strings.Index(body, "Busy Till")
	idleIdx := strings.Index(body, "Idle Till")
	if busyIdx == -1 || idleIdx == -1 {
		t.Fatalf("both registers must be listed, got: %s", body)
	}
	const hint = "Has shift/sale history"
	rowFor := func(nameIdx int) string {
		// A table row is short; 400 chars comfortably spans one row's
		// cells without reaching into the next register's row.
		end := nameIdx + 400
		if end > len(body) {
			end = len(body)
		}
		return body[nameIdx:end]
	}
	if !strings.Contains(rowFor(busyIdx), hint) {
		t.Fatalf("Busy Till (has a sale) must show the in-use hint, row: %s", rowFor(busyIdx))
	}
	if strings.Contains(rowFor(idleIdx), hint) {
		t.Fatalf("Idle Till (no history) must NOT show the in-use hint, row: %s", rowFor(idleIdx))
	}
}

// A shop must always have at least one active register to open a shift or
// take a sale on -- mirrors the last-active-location guard on stock
// locations.
func TestRegistersPage_CannotDeactivateLastActiveRegister(t *testing.T) {
	mux, d := newRegistersTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	rec := postForm(mux, "/api/registers", url.Values{"name": {"Only Till"}}, &manager)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create: code=%d", rec.Code)
	}
	var onlyID string
	if err := d.Db.QueryRow(`SELECT id FROM registers WHERE name = 'Only Till'`).Scan(&onlyID); err != nil {
		t.Fatalf("lookup: %v", err)
	}

	rec = postForm(mux, "/api/registers/"+onlyID+"/active", url.Values{"active": {"0"}}, &manager)
	if rec.Header().Get("Location") != "/registers?err=registers.error.last_active" {
		t.Fatalf("last-active guard: loc=%q", rec.Header().Get("Location"))
	}
	var active int
	if err := d.Db.QueryRow(`SELECT is_active FROM registers WHERE id = ?`, onlyID).Scan(&active); err != nil || active != 1 {
		t.Fatalf("must remain active: active=%d err=%v", active, err)
	}
}

// ut-docs#903: a manager granted "settings" but NOT the new dedicated
// "stock_location_management" action must be denied here -- see
// locations_page_test.go's identical test for the full rationale.
func TestRegistersPage_DeniedWithSettingsButNotStockLocationManagement(t *testing.T) {
	mux, d := newRegistersTestMux(t)
	authRepo := data.NewAuthRepo(d.Db)
	ctx := t.Context()

	if err := authRepo.SetRolePermission(ctx, nil, "manager", "stock_location_management", false); err != nil {
		t.Fatal(err)
	}

	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/registers", nil), manager)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("manager with settings but not stock_location_management: GET /registers = %d, want 403", rec.Code)
	}
}

// ut-docs#2116: /registers is one of the /admin tree's six destinations,
// converted to the same two-pane master-detail shell /items uses
// (ut-docs#1950). An htmx request (from that panel) must get just the
// "content" block, plus an out-of-band refresh of the admin tree with
// /registers marked is-current — not the full standalone page's chrome.
func TestRegistersPage_HXRequestReturnsContentFragmentWithOOBAdminTree(t *testing.T) {
	mux, _ := newRegistersTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/registers", nil), manager)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("htmx GET /registers: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "<html") || strings.Contains(body, `class="nav"`) {
		t.Errorf("htmx request re-rendered the whole page shell: %s", body)
	}
	railStart := strings.Index(body, `id="admin-tree"`)
	if railStart < 0 || !strings.Contains(body, `hx-swap-oob="true"`) {
		t.Fatalf("fragment missing the OOB admin-tree swap: %s", body)
	}
	rail := body[railStart:]
	idx := strings.Index(rail, `href="/registers"`)
	if idx < 0 {
		t.Fatalf("OOB admin tree missing the /registers row: %s", rail)
	}
	tagStart := strings.LastIndex(rail[:idx], "<a ")
	tagEnd := strings.Index(rail[tagStart:], ">") + tagStart
	if !strings.Contains(rail[tagStart:tagEnd], "is-current") {
		t.Errorf("the /registers row itself is not marked is-current: %s", rail[tagStart:tagEnd])
	}
}

// A plain browser GET (no HX-Request) must still render the exact same full
// standalone page as before this card.
func TestRegistersPage_NonHXRequestStillRendersFullPage(t *testing.T) {
	mux, _ := newRegistersTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/registers", nil), manager)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /registers: %d %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "<html") || !strings.Contains(body, `class="nav"`) {
		t.Errorf("expected the full standalone page shell, got: %s", body)
	}
}

// ut-docs#2091's Vary requirement, extended to /registers now that it is a
// dual-mode destination too.
func TestRegistersPage_VaryHXRequestOnBothBranches(t *testing.T) {
	mux, _ := newRegistersTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	fragReq := auth.WithUser(httptest.NewRequest(http.MethodGet, "/registers", nil), manager)
	fragReq.Header.Set("HX-Request", "true")
	fragRec := httptest.NewRecorder()
	mux.ServeHTTP(fragRec, fragReq)
	if got := fragRec.Header().Get("Vary"); got != "HX-Request" {
		t.Errorf("fragment branch: Vary header = %q, want %q", got, "HX-Request")
	}

	fullReq := auth.WithUser(httptest.NewRequest(http.MethodGet, "/registers", nil), manager)
	fullRec := httptest.NewRecorder()
	mux.ServeHTTP(fullRec, fullReq)
	if got := fullRec.Header().Get("Vary"); got != "HX-Request" {
		t.Errorf("full-page branch: Vary header = %q, want %q", got, "HX-Request")
	}
}

// Independent review of ut-docs#2116: the six new dual-mode handlers all
// route through httpx.IsFragmentSwap, so they inherit its
// HX-History-Restore-Request exclusion (ut-docs#433/#2091) — htmx re-requests
// a restored history entry with BOTH headers set and expects the FULL page
// back, and answering that with a bare fragment leaves the restored screen
// chrome-less. That rule was pinned only on /help and /inventory before this
// card; nothing pinned it on any of the six, so a future handler-local
// "just check HX-Request" simplification would go unnoticed here. Covered
// once, on /registers, exactly as the /items rail covers it once on
// /inventory (TestInventoryPage_HXHistoryRestoreReturnsFullPage).
func TestRegistersPage_HXHistoryRestoreReturnsFullPage(t *testing.T) {
	mux, _ := newRegistersTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/registers", nil), manager)
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-History-Restore-Request", "true")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("history-restore GET /registers: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<html") || !strings.Contains(body, `class="nav"`) {
		t.Errorf("a history-restore request must get the full standalone page, got: %s", body)
	}
	if strings.Contains(body, `hx-swap-oob=`) {
		t.Errorf("a history-restore request must not carry the OOB admin-tree swap: %s", body)
	}
}

// ut-docs#2185: /registers adopts the record_dialog/list_header pattern
// (ut-docs#2010), mirroring locations_page_test.go's htmx-path coverage
// for its near-twin (ut-docs#2124), which itself mirrors
// categories_page_test.go's original proof. postFormHtmx is
// categories_page_test.go's shared htmx-boosted POST helper, same
// package, reused as-is.
func TestRegistersPage_RefusalRendersInDialogMessageForHtmxRequest(t *testing.T) {
	mux, _ := newRegistersTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	rec := postFormHtmx(mux, "/api/registers", url.Values{"name": {"   "}}, &manager)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("htmx whitespace-only name: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Location") != "" {
		t.Errorf("htmx refusal must not redirect — a redirect is exactly what closed the dialog before this card")
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	body := rec.Body.String()
	if strings.Contains(body, "id=") || strings.Contains(body, "<form") || strings.Contains(body, "<dialog") {
		t.Errorf("response must be ONLY the message text — no wrapper, form or dialog markup: %s", body)
	}
}

func TestRegistersPage_HtmxSuccessAnswersWithHXRedirectNotBareRedirect(t *testing.T) {
	mux, _ := newRegistersTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	rec := postFormHtmx(mux, "/api/registers", url.Values{"name": {"Loading Bay Till"}}, &manager)
	if rec.Code != http.StatusOK {
		t.Fatalf("htmx create success: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("HX-Redirect"); got != "/registers" {
		t.Fatalf("HX-Redirect = %q, want /registers", got)
	}
	if rec.Header().Get("Location") != "" {
		t.Errorf("a bare Location alongside HX-Redirect would be followed by the boosted form's own fetch/XHR layer")
	}
}

func TestRegistersPage_ReplicaRefusalRendersInDialogMessageForHtmxRequest(t *testing.T) {
	mux, d := newRegistersTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	if err := d.Settings.Set(t.Context(), "sync.primary_url", "http://primary.example"); err != nil {
		t.Fatalf("set primary_url: %v", err)
	}

	rec := postFormHtmx(mux, "/api/registers", url.Values{"name": {"Satellite Till"}}, &manager)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("htmx create on replica: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Location") != "" {
		t.Errorf("htmx replica refusal must not redirect")
	}
}
