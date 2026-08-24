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
)

func newRegistersTestMux(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)

	d := &common.Deps{Db: db, Menu: []common.MenuItem{{Href: "/", Label: "Home"}}, AuthSvc: auth.NewService(db)}
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

func TestRegistersPagePermissions(t *testing.T) {
	mux, _ := newRegistersTestMux(t)
	cashier := auth.User{ID: "c1", Role: "cashier", DisplayName: "Cash"}

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/registers", nil), cashier)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cashier GET /registers = %d, want 403", rec.Code)
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
	if _, err := d.Db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc_back','Back Room')`); err != nil {
		t.Fatalf("seed second location: %v", err)
	}

	// Move it to a different location.
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
