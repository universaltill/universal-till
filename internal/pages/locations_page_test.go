package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
)

func newLocationsTestMux(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)

	d := &common.Deps{Db: db, Menu: []common.MenuItem{{Href: "/", Label: "Home"}}, AuthSvc: auth.NewService(db)}
	mux := http.NewServeMux()
	registerLocations(mux, d)
	return mux, d
}

func TestLocationsPagePermissions(t *testing.T) {
	mux, _ := newLocationsTestMux(t)
	cashier := auth.User{ID: "c1", Role: "cashier", DisplayName: "Cash"}

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/locations", nil), cashier)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cashier GET /locations = %d, want 403", rec.Code)
	}

	if rec := postForm(mux, "/api/locations", url.Values{"name": {"Cellar"}}, &cashier); rec.Code != http.StatusForbidden {
		t.Fatalf("cashier create = %d, want 403", rec.Code)
	}
}

// ut-docs#901: GET /locations must be reachable under UT_AUTH=off with no
// session, matching every other admin page's canPerform(..., "settings")
// escape hatch (and matching /users, already migrated). Before this fix,
// requireManager read auth.FromContext directly and failed closed
// permanently under UT_AUTH=off, since no session is ever set in that mode.
func TestLocationsPage_ReachableUnderAuthOff(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newLocationsTestMux(t)

	req := httptest.NewRequest(http.MethodGet, "/locations", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /locations under UT_AUTH=off = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

// Mutating handlers, not just the GET page, must also pick up canPerform's
// UT_AUTH=off bypass -- independent review finding, ut-docs#901 (the
// original regression test above only pinned the read path).
func TestLocationsPageCreate_ReachableUnderAuthOff(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newLocationsTestMux(t)

	rec := postForm(mux, "/api/locations", url.Values{"name": {"Auth-Off Room"}}, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/locations" {
		t.Fatalf("create under UT_AUTH=off: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestLocationsPageCreate_WhitespaceOnlyNameRejected(t *testing.T) {
	mux, _ := newLocationsTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	rec := postForm(mux, "/api/locations", url.Values{"name": {"   "}}, &manager)
	if rec.Header().Get("Location") != "/locations?err=locations.error.required" {
		t.Fatalf("whitespace-only name: loc=%q", rec.Header().Get("Location"))
	}
}

func TestLocationsPageCreateRenameDeactivate(t *testing.T) {
	mux, d := newLocationsTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	// Create.
	rec := postForm(mux, "/api/locations", url.Values{"name": {"Back Room"}}, &manager)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/locations" {
		t.Fatalf("create: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}

	// It shows up on the rendered page.
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/locations", nil), manager)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Back Room") {
		t.Fatalf("locations page missing new location: code=%d body=%s", rec.Code, rec.Body.String())
	}

	// Duplicate name is rejected.
	rec = postForm(mux, "/api/locations", url.Values{"name": {"Back Room"}}, &manager)
	if rec.Header().Get("Location") != "/locations?err=locations.error.create" {
		t.Fatalf("duplicate create: loc=%q", rec.Header().Get("Location"))
	}

	// Find the id we just created (the seed's loc_main is the other row).
	var newID string
	if err := d.Db.QueryRow(`SELECT id FROM stock_locations WHERE name = 'Back Room'`).Scan(&newID); err != nil {
		t.Fatalf("lookup: %v", err)
	}

	// Rename.
	rec = postForm(mux, "/api/locations/"+newID, url.Values{"name": {"Back Room Renamed"}}, &manager)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("rename: code=%d", rec.Code)
	}
	var name string
	if err := d.Db.QueryRow(`SELECT name FROM stock_locations WHERE id = ?`, newID).Scan(&name); err != nil || name != "Back Room Renamed" {
		t.Fatalf("rename did not take effect: name=%q err=%v", name, err)
	}

	// A location with no inventory/movement/register history deactivates cleanly.
	rec = postForm(mux, "/api/locations/"+newID+"/active", url.Values{"active": {"0"}}, &manager)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/locations" {
		t.Fatalf("deactivate: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
	var active int
	if err := d.Db.QueryRow(`SELECT is_active FROM stock_locations WHERE id = ?`, newID).Scan(&active); err != nil || active != 0 {
		t.Fatalf("not deactivated: active=%d err=%v", active, err)
	}

	// loc_main is seeded with real inventory — deactivating it is refused.
	rec = postForm(mux, "/api/locations/loc_main/active", url.Values{"active": {"0"}}, &manager)
	if rec.Header().Get("Location") != "/locations?err=locations.error.in_use" {
		t.Fatalf("in-use guard: loc=%q", rec.Header().Get("Location"))
	}
	if err := d.Db.QueryRow(`SELECT is_active FROM stock_locations WHERE id = 'loc_main'`).Scan(&active); err != nil || active != 1 {
		t.Fatalf("loc_main must remain active: active=%d err=%v", active, err)
	}
}

// A shop must always have at least one active location to receive/adjust/
// return stock into — mirrors the last-active-admin guard on users.
func TestLocationsPage_CannotDeactivateLastActiveLocation(t *testing.T) {
	mux, d := newLocationsTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	rec := postForm(mux, "/api/locations", url.Values{"name": {"Only Location"}}, &manager)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create: code=%d", rec.Code)
	}
	var onlyID string
	if err := d.Db.QueryRow(`SELECT id FROM stock_locations WHERE name = 'Only Location'`).Scan(&onlyID); err != nil {
		t.Fatalf("lookup: %v", err)
	}
	// Simulate a shop that's already down to this one active location
	// (bypassing the handler — loc_main has seeded inventory so the
	// in-use guard would otherwise refuse deactivating it first).
	if _, err := d.Db.Exec(`UPDATE stock_locations SET is_active = 0 WHERE id = 'loc_main'`); err != nil {
		t.Fatalf("seed pre-state: %v", err)
	}

	rec = postForm(mux, "/api/locations/"+onlyID+"/active", url.Values{"active": {"0"}}, &manager)
	if rec.Header().Get("Location") != "/locations?err=locations.error.last_location" {
		t.Fatalf("last-location guard: loc=%q", rec.Header().Get("Location"))
	}
	var active int
	if err := d.Db.QueryRow(`SELECT is_active FROM stock_locations WHERE id = ?`, onlyID).Scan(&active); err != nil || active != 1 {
		t.Fatalf("must remain active: active=%d err=%v", active, err)
	}
}

// Deactivating is only meaningful if it actually changes what the rest of
// the app offers — the inventory page's stock-adjust location picker must
// stop offering a deactivated location as a destination.
func TestLocationsPage_DeactivatedLocationHiddenFromInventoryPicker(t *testing.T) {
	mux, d := newLocationsTestMux(t)
	registerInventoryPage(mux, d)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	rec := postForm(mux, "/api/locations", url.Values{"name": {"Pop-up Stall"}}, &manager)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create: code=%d", rec.Code)
	}
	var newID string
	if err := d.Db.QueryRow(`SELECT id FROM stock_locations WHERE name = 'Pop-up Stall'`).Scan(&newID); err != nil {
		t.Fatalf("lookup: %v", err)
	}

	getInventoryPage := func() string {
		req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/inventory", nil), manager)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /inventory: code=%d body=%s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}

	if !strings.Contains(getInventoryPage(), "Pop-up Stall") {
		t.Fatal("active new location should appear in the inventory picker")
	}

	if rec := postForm(mux, "/api/locations/"+newID+"/active", url.Values{"active": {"0"}}, &manager); rec.Code != http.StatusSeeOther {
		t.Fatalf("deactivate: code=%d", rec.Code)
	}

	if strings.Contains(getInventoryPage(), "Pop-up Stall") {
		t.Fatal("deactivated location must no longer appear in the inventory picker")
	}
}

// ut-docs#903: a manager granted "settings" but NOT the new dedicated
// "stock_location_management" action must be denied here -- pins that the
// two actions are genuinely independent now, not just that the seeded
// default (both granted together) still works. Before #903 this scenario
// was unrepresentable: reusing "settings" meant granting it always granted
// this page too.
func TestLocationsPage_DeniedWithSettingsButNotStockLocationManagement(t *testing.T) {
	mux, d := newLocationsTestMux(t)
	authRepo := data.NewAuthRepo(d.Db)
	ctx := t.Context()

	if err := authRepo.SetRolePermission(ctx, nil, "manager", "stock_location_management", false); err != nil {
		t.Fatal(err)
	}

	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/locations", nil), manager)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("manager with settings but not stock_location_management: GET /locations = %d, want 403", rec.Code)
	}
}
