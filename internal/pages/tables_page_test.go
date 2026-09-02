package pages

// Table floor plan page (universaltill/ut-docs#814, ADR-0054): manager
// gating, table CRUD + drag position saves via the form endpoints, and a
// real render of web/ui/pages/tables.html plus the live-state SVG partial.
//
// Until ut-docs#820 wires table assignment onto held sales, every table
// renders free — the partial's "occupied" branch is proven by seeding a
// held_sales row with table_id set directly.

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
)

func newTablesTestMux(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	dbase, err := db.Open(filepath.Join(t.TempDir(), "tables-page.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dbase.Close() })
	d := &common.Deps{Db: dbase.DB, Settings: settings.NewStore(dbase.DB), Menu: []common.MenuItem{{Href: "/", Label: "Home"}}, AuthSvc: auth.NewService(dbase.DB)}
	mux := http.NewServeMux()
	registerTables(mux, d)
	return mux, d
}

// ut-docs#902: GET /tables must be reachable under UT_AUTH=off with no
// session — same fix and rationale as
// country_settings_page_test.go's TestCountrySettingsPage_ReachableUnderAuthOff
// (ut-docs#901's precedent).
func TestTablesPage_ReachableUnderAuthOff(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newTablesTestMux(t)

	req := httptest.NewRequest(http.MethodGet, "/tables", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /tables under UT_AUTH=off = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

// Mutating handlers, not just the GET page, must also pick up canPerform's
// UT_AUTH=off bypass — independent review finding on ut-docs#901, applied
// here too.
func TestTablesPageCreate_ReachableUnderAuthOff(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newTablesTestMux(t)

	rec := postForm(mux, "/api/tables", url.Values{"label": {"Auth-Off Table"}, "shape": {"rect"}}, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/tables" {
		t.Fatalf("create under UT_AUTH=off: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestTablesPagePermissions(t *testing.T) {
	mux, _ := newTablesTestMux(t)
	cashier := auth.User{ID: "c1", Role: "cashier", DisplayName: "Cash"}

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/tables", nil), cashier)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cashier GET /tables = %d, want 403", rec.Code)
	}
	if rec := postForm(mux, "/api/tables", url.Values{"label": {"T1"}}, &cashier); rec.Code != http.StatusForbidden {
		t.Fatalf("cashier create = %d, want 403", rec.Code)
	}
	if rec := postForm(mux, "/api/tables/x/position", url.Values{"pos_x": {"1"}, "pos_y": {"1"}}, &cashier); rec.Code != http.StatusForbidden {
		t.Fatalf("cashier position = %d, want 403", rec.Code)
	}
}

// TestTablesPage_RepoErrorRendersFullLayout is the regression test for the
// live bug ut-docs#1455 fixes: ListTablesWithState failing used to fall
// back to a bare-text http.Error body — no nav rail, no way back on the
// pinned Android kiosk (no browser Back). Force the repo call to fail
// (drop the underlying table, same fault-injection pattern
// catalog/handlers_errors_test.go already uses) and assert the response is
// still the full page layout — nav rail + Back to sale — not a bare body.
func TestTablesPage_RepoErrorRendersFullLayout(t *testing.T) {
	mux, d := newTablesTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	if _, err := d.Db.Exec(`DROP TABLE tables`); err != nil {
		t.Fatalf("drop tables table: %v", err)
	}

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/tables", nil), manager)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("GET /tables with broken repo = %d, want 500: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "nav-toggle") {
		t.Fatalf("500 response missing nav rail marker (nav-toggle) — bare-text lock-up regression; body:\n%s", body)
	}
	if !strings.Contains(body, "Back to sale") {
		t.Fatalf("500 response missing \"Back to sale\" link; body:\n%s", body)
	}
}

func TestTablesPage_CreateEditPositionDeactivateAndRender(t *testing.T) {
	mux, d := newTablesTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	// Empty floor plan: the page still renders, with the empty-state prompt
	// (soft gate — zero tables configured is a valid state, ADR-0054).
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/tables", nil), manager)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("empty page render: code=%d", rec.Code)
	}

	// Whitespace-only name rejected.
	rec = postForm(mux, "/api/tables", url.Values{"label": {"   "}, "shape": {"rect"}}, &manager)
	if rec.Header().Get("Location") != "/tables?err=tables.error.required" {
		t.Fatalf("whitespace-only name: loc=%q", rec.Header().Get("Location"))
	}
	// Invalid shape rejected.
	rec = postForm(mux, "/api/tables", url.Values{"label": {"T1"}, "shape": {"hex"}}, &manager)
	if rec.Header().Get("Location") != "/tables?err=tables.error.shape" {
		t.Fatalf("invalid shape: loc=%q", rec.Header().Get("Location"))
	}

	// Create.
	rec = postForm(mux, "/api/tables", url.Values{
		"label": {"T1"}, "area_zone": {"Terrace"}, "seat_count": {"4"}, "shape": {"rect"},
	}, &manager)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/tables" {
		t.Fatalf("create: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
	var id string
	if err := d.Db.QueryRow(`SELECT id FROM tables WHERE label = 'T1'`).Scan(&id); err != nil {
		t.Fatalf("lookup: %v", err)
	}

	// The page renders the table on the plan.
	req = auth.WithUser(httptest.NewRequest(http.MethodGet, "/tables", nil), manager)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "T1") {
		t.Fatalf("page render: code=%d body missing table", rec.Code)
	}

	// Edit name/zone/seats/shape.
	rec = postForm(mux, "/api/tables/"+id, url.Values{
		"label": {"Table 1"}, "area_zone": {"Main"}, "seat_count": {"6"}, "shape": {"round"},
	}, &manager)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("update: code=%d", rec.Code)
	}
	var label, zone, shape string
	var seats int
	if err := d.Db.QueryRow(`SELECT label, area_zone, seat_count, shape FROM tables WHERE id = ?`, id).
		Scan(&label, &zone, &seats, &shape); err != nil || label != "Table 1" || zone != "Main" || seats != 6 || shape != "round" {
		t.Fatalf("update did not take effect: %q %q %d %q err=%v", label, zone, seats, shape, err)
	}

	// Drag-end position save (the JS posts here): 204, no redirect.
	rec = postForm(mux, "/api/tables/"+id+"/position", url.Values{"pos_x": {"640"}, "pos_y": {"480"}}, &manager)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("position save: code=%d", rec.Code)
	}
	var px, py int
	if err := d.Db.QueryRow(`SELECT pos_x, pos_y FROM tables WHERE id = ?`, id).Scan(&px, &py); err != nil || px != 640 || py != 480 {
		t.Fatalf("position not persisted: %d,%d err=%v", px, py, err)
	}
	// Garbage coordinates → 400, position unchanged.
	rec = postForm(mux, "/api/tables/"+id+"/position", url.Values{"pos_x": {"abc"}, "pos_y": {"1"}}, &manager)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad position: code=%d", rec.Code)
	}
	// Unknown id → 404.
	rec = postForm(mux, "/api/tables/nope/position", url.Values{"pos_x": {"1"}, "pos_y": {"1"}}, &manager)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("position on missing table: code=%d", rec.Code)
	}

	// Unknown id on update → not_found error key.
	rec = postForm(mux, "/api/tables/nope", url.Values{"label": {"X"}, "shape": {"rect"}}, &manager)
	if rec.Header().Get("Location") != "/tables?err=tables.error.not_found" {
		t.Fatalf("not-found update: loc=%q", rec.Header().Get("Location"))
	}

	// Deactivate / reactivate (soft toggle, no delete).
	rec = postForm(mux, "/api/tables/"+id+"/active", url.Values{"active": {"0"}}, &manager)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("deactivate: code=%d", rec.Code)
	}
	var enabled int
	if err := d.Db.QueryRow(`SELECT enabled FROM tables WHERE id = ?`, id).Scan(&enabled); err != nil || enabled != 0 {
		t.Fatalf("not deactivated: enabled=%d err=%v", enabled, err)
	}
	rec = postForm(mux, "/api/tables/"+id+"/active", url.Values{"active": {"1"}}, &manager)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("reactivate: code=%d", rec.Code)
	}
}

// Tap-to-place (ut-docs#1025): POST /api/tables optionally carries the
// tapped canvas position as pos_x/pos_y. Present-and-valid → the table is
// created there (repo-clamped to the canvas); absent, empty, or garbage →
// the pre-#1025 behaviour, canvas centre — which is exactly what the
// bottom-of-page add form (which never sends these fields) relies on.
func TestTablesPageCreate_TapToPlacePosition(t *testing.T) {
	mux, d := newTablesTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	posOf := func(label string) (x, y int) {
		t.Helper()
		if err := d.Db.QueryRow(`SELECT pos_x, pos_y FROM tables WHERE label = ?`, label).Scan(&x, &y); err != nil {
			t.Fatalf("lookup %q: %v", label, err)
		}
		return x, y
	}
	create := func(form url.Values) {
		t.Helper()
		rec := postForm(mux, "/api/tables", form, &manager)
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/tables" {
			t.Fatalf("create %v: code=%d loc=%q", form, rec.Code, rec.Header().Get("Location"))
		}
	}
	centre := data.TableCanvasSize / 2

	// Tapped position is used verbatim (in-canvas, so unclamped).
	create(url.Values{"label": {"Tapped"}, "shape": {"rect"}, "pos_x": {"200"}, "pos_y": {"300"}})
	if x, y := posOf("Tapped"); x != 200 || y != 300 {
		t.Fatalf("tapped position: got %d,%d want 200,300", x, y)
	}

	// Off-plan values are clamped by the repo (clampToCanvas), never trusted.
	create(url.Values{"label": {"Clamped"}, "shape": {"rect"}, "pos_x": {"-50"}, "pos_y": {"5000"}})
	wantX, wantY := data.TableEdgeInset, data.TableCanvasSize-data.TableEdgeInset
	if x, y := posOf("Clamped"); x != wantX || y != wantY {
		t.Fatalf("clamped position: got %d,%d want %d,%d", x, y, wantX, wantY)
	}

	// No pos fields at all (the existing bottom-of-page form) → canvas centre,
	// byte-for-byte the pre-#1025 behaviour.
	create(url.Values{"label": {"Centre"}, "shape": {"rect"}})
	if x, y := posOf("Centre"); x != centre || y != centre {
		t.Fatalf("no pos fields: got %d,%d want %d,%d", x, y, centre, centre)
	}

	// Unparseable values fall back to centre too — never an error, never 0,0.
	create(url.Values{"label": {"Garbage"}, "shape": {"rect"}, "pos_x": {"abc"}, "pos_y": {""}})
	if x, y := posOf("Garbage"); x != centre || y != centre {
		t.Fatalf("garbage pos fields: got %d,%d want %d,%d", x, y, centre, centre)
	}
}

// The live-state SVG partial: today (pre-#820) every table renders in its
// free state; a held sale carrying table_id flips the tile to occupied with
// an elapsed-minutes label.
func TestTablesStatePartial_FreeTodayOccupiedViaTableID(t *testing.T) {
	mux, d := newTablesTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	rec := postForm(mux, "/api/tables", url.Values{
		"label": {"T1"}, "seat_count": {"4"}, "shape": {"rect"},
	}, &manager)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create: code=%d", rec.Code)
	}

	get := func() string {
		req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/ui/tables/state", nil), manager)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("state partial: code=%d", rec.Code)
		}
		return rec.Body.String()
	}

	body := get()
	if !strings.Contains(body, "<svg") || !strings.Contains(body, "T1") {
		t.Fatalf("state partial must render the SVG plan with the table: %q", body)
	}
	if !strings.Contains(body, "table-free") || strings.Contains(body, "table-occupied") {
		t.Fatalf("pre-#820 every table must render free: %q", body)
	}

	// Occupied branch: an open order assigned to the table (as #820 will
	// write it), opened 30 minutes ago.
	var id string
	if err := d.Db.QueryRow(`SELECT id FROM tables WHERE label = 'T1'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	since := time.Now().UTC().Add(-30 * time.Minute).Format("2006-01-02 15:04:05")
	if _, err := d.Db.Exec(
		`INSERT INTO held_sales (id, label, total_minor, line_count, payload, table_id, created_at)
		 VALUES ('h1','',0,0,'{}',?,?)`, id, since); err != nil {
		t.Fatal(err)
	}
	body = get()
	if !strings.Contains(body, "table-occupied") {
		t.Fatalf("assigned table must render occupied: %q", body)
	}
	if !strings.Contains(body, "30") {
		t.Fatalf("occupied tile must show elapsed minutes: %q", body)
	}
}

// elapsedMinutes parses both timestamp formats live held_sales rows carry:
// the schema default (datetime('now') → "2006-01-02 15:04:05", UTC) and
// RFC3339 in case a writer ever sets it explicitly.
func TestTablesElapsedMinutes(t *testing.T) {
	now := time.Date(2026, 8, 18, 11, 0, 0, 0, time.UTC)
	if m := elapsedMinutes("2026-08-18 10:30:00", now); m != 30 {
		t.Fatalf("sqlite format: got %d, want 30", m)
	}
	if m := elapsedMinutes("2026-08-18T09:45:00Z", now); m != 75 {
		t.Fatalf("rfc3339: got %d, want 75", m)
	}
	if m := elapsedMinutes("", now); m != 0 {
		t.Fatalf("empty: got %d, want 0", m)
	}
	if m := elapsedMinutes("garbage", now); m != 0 {
		t.Fatalf("garbage: got %d, want 0", m)
	}
	// A clock skew putting the order "in the future" must not render a
	// negative elapsed time.
	if m := elapsedMinutes("2026-08-18 11:30:00", now); m != 0 {
		t.Fatalf("future: got %d, want 0", m)
	}
}
