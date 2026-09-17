package catalog

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	appdb "github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
)

// newCatalogMuxRealSession builds Register(mux, d) over the REAL migrated
// schema (internal/db.Open, same runner every till actually boots through)
// with a real auth.Service wired -- UNLIKE this package's other fixtures
// (testsupport.NewCatalogTestDB's hand-rolled schema has no users/
// role_permissions tables at all, so it cannot exercise a real session
// gate). ut-docs#2312: requireCatalogManagement (handlers.go) is the first
// thing every mutating handler in this package now checks; this proves it
// against the REAL role_permissions seed (migration 001_init.sql), not a
// hand-mirrored fixture, the same reasoning
// internal/db/catalog_management_permission_test.go documents for the
// migration-seed side of this same card.
func newCatalogMuxRealSession(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	chdirToRepoRoot(t)
	dbPath := filepath.Join(t.TempDir(), "catalog-gate.db")
	migrated, err := appdb.Open(dbPath)
	if err != nil {
		t.Fatalf("open migrated db: %v", err)
	}
	t.Cleanup(func() { migrated.Close() })
	sqlDB := migrated.DB
	d := &common.Deps{
		Db:       sqlDB,
		State:    common.RuntimeState{Theme: "default", Currency: "GBP"},
		Menu:     []common.MenuItem{},
		Settings: settings.NewStore(sqlDB),
		AuthSvc:  auth.NewService(sqlDB),
	}
	mux := http.NewServeMux()
	Register(mux, d)
	return mux, d
}

// ut-docs#2312 AC: "A cashier-role session gets 403/elevation prompt on
// every route above; manager passes." This is the catalog/handlers.go half
// -- every mutating /api/catalog/* route, one entry per mux.HandleFunc site
// this card added requireCatalogManagement to. Deliberately t.Errorf, not
// t.Fatalf (same reasoning as TestCatalogItemMutations_RefusedOnReplica,
// this package's sibling test): one broken route must not hide another.
//
// Payloads are minimal/empty on purpose -- requireCatalogManagement runs
// BEFORE any form parsing or business validation in every handler, so a
// cashier must be refused regardless of what the body contains. A manager
// request may still fail business validation afterwards (e.g. "id
// required") -- this test only proves it gets PAST the permission gate
// (never a 403), matching TestImportExportEndpoints_RealSessionGatesByRole's
// own "not 403" contract, not a full round-trip of every route's own logic
// (that's this package's existing, per-route test files' job).
func TestCatalogHandlers_CatalogManagementGate_RealSessionGatesByRole(t *testing.T) {
	// main_test.go's TestMain sets UT_AUTH=off process-wide so this
	// package's ~28 pre-existing, auth-agnostic test files keep passing
	// unchanged (see its own doc comment) -- THIS test is exactly the one
	// that must override that back to "on" to exercise the real gate.
	t.Setenv("UT_AUTH", "on")
	mux, _ := newCatalogMuxRealSession(t)

	routes := []string{
		"/api/catalog/item-cost",
		"/api/catalog/item-lead-time",
		"/api/catalog/item-reorder-level",
		"/api/catalog/option-set",
		"/api/catalog/option-set-value",
		"/api/catalog/item/option-sets",
		"/api/catalog/item/generate-variants",
		"/api/catalog/item",
		"/api/catalog/item/update",
		"/api/catalog/item/deactivate",
		"/api/catalog/variant",
		"/api/catalog/modifier-group",
		"/api/catalog/modifier-option",
		"/api/catalog/modifier-group/attach",
		"/api/catalog/modifier-group/detach",
		"/api/catalog/modifier-group/opt-out",
		"/api/catalog/modifier-group/opt-in",
		"/api/catalog/item-station-routes",
		"/api/catalog/variant/deactivate",
		"/api/catalog/item/image",
		"/api/catalog/item/icon",
		"/api/catalog/variant/image",
		"/api/catalog/barcode",
		"/api/catalog/barcode/delete",
		"/api/catalog/barcode-backfill",
	}

	post := func(path string, u auth.User) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(url.Values{}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req = auth.WithUser(req, u)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	cashier := auth.User{ID: "c1", Role: "cashier"}
	for _, path := range routes {
		rec := post(path, cashier)
		if rec.Code != http.StatusForbidden {
			t.Errorf("cashier POST %s = %d, want 403: %s", path, rec.Code, rec.Body.String())
		}
	}

	for _, role := range []string{"manager", "admin", "super_admin"} {
		mgr := auth.User{ID: "u-" + role, Role: role}
		for _, path := range routes {
			rec := post(path, mgr)
			if rec.Code == http.StatusForbidden {
				t.Errorf("%s POST %s = 403, want past the catalog_management gate: %s", role, path, rec.Body.String())
			}
		}
	}
}

// No session at all (not even UT_AUTH=off) must be refused exactly like a
// cashier -- canPerform/requireCatalogManagement's own fail-closed
// contract (elevation.go/authz.go's identical shape) applies here too.
func TestCatalogHandlers_CatalogManagementGate_NoSessionDenied(t *testing.T) {
	t.Setenv("UT_AUTH", "on")
	mux, _ := newCatalogMuxRealSession(t)
	req := httptest.NewRequest(http.MethodPost, "/api/catalog/item", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("no-session POST /api/catalog/item = %d, want 403: %s", rec.Code, rec.Body.String())
	}
}

// ut-docs#2357: the three full-page GET routes in this file (/catalog,
// /modifiers, /catalog/option-sets) carried no server-side gate at all
// before this card -- only their nav tiles were hidden via VisibleIf. Same
// contract and same real migrated-schema/real-session rig as
// TestCatalogHandlers_CatalogManagementGate_RealSessionGatesByRole above,
// just for requireCatalogManagementPage instead of requireCatalogManagement
// (the httpx.RenderError-vs-LocalizedError split handlers.go documents on
// that helper).
func TestCatalogHandlers_CatalogManagementGate_PageRoutes(t *testing.T) {
	t.Setenv("UT_AUTH", "on")
	mux, _ := newCatalogMuxRealSession(t)

	pageRoutes := []string{"/catalog", "/modifiers", "/catalog/option-sets"}

	get := func(path string, u auth.User) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req = auth.WithUser(req, u)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	cashier := auth.User{ID: "c1", Role: "cashier"}
	for _, path := range pageRoutes {
		rec := get(path, cashier)
		if rec.Code != http.StatusForbidden {
			t.Errorf("cashier GET %s = %d, want 403: %s", path, rec.Code, rec.Body.String())
		}
		// Same rail-must-survive-a-403 rule locations_page_test.go's
		// TestLocationsPagePermissions pins for its own page gate
		// (ut-docs#1458): a page route's 403 renders the full themed error
		// page, never a bare, rail-less body.
		if body := rec.Body.String(); !strings.Contains(body, `class="nav"`) {
			t.Errorf("cashier's 403 on GET %s has no nav rail:\n%s", path, body)
		}
	}

	for _, role := range []string{"manager", "admin", "super_admin"} {
		mgr := auth.User{ID: "u-" + role, Role: role}
		for _, path := range pageRoutes {
			if rec := get(path, mgr); rec.Code == http.StatusForbidden {
				t.Errorf("%s GET %s = 403, want past the catalog_management gate: %s", role, path, rec.Body.String())
			}
		}
	}
}

// ut-docs#2374: six GET /api/catalog/* fragment endpoints carried no gate
// at all -- #2357 only gated the three full-PAGE GET routes above, and this
// package's mutating routes were already gated (ut-docs#2312), but these
// six read-only fragments were missed entirely. Same disclosure class as
// both prior cards, same real migrated-schema/real-session rig, just GET
// with requireCatalogManagement's plain common.LocalizedError response
// (these are HTMX/API fragments, not full pages, so no
// requireCatalogManagementPage/httpx.RenderError here).
func TestCatalogHandlers_CatalogManagementGate_GETFragmentRoutes(t *testing.T) {
	t.Setenv("UT_AUTH", "on")
	mux, _ := newCatalogMuxRealSession(t)

	fragmentRoutes := []string{
		"/api/catalog/lookup?barcode=5449000000996",
		"/api/catalog/modifier-groups-panel?item_id=itm1",
		"/api/catalog/variant-options?item_id=itm1",
		"/api/catalog/item-variants?item_id=itm1",
		"/api/catalog/item/icon-state?item_id=itm1",
		"/api/catalog/barcode-backfill",
	}

	get := func(path string, u auth.User) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req = auth.WithUser(req, u)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	cashier := auth.User{ID: "c1", Role: "cashier"}
	for _, path := range fragmentRoutes {
		rec := get(path, cashier)
		if rec.Code != http.StatusForbidden {
			t.Errorf("cashier GET %s = %d, want 403: %s", path, rec.Code, rec.Body.String())
		}
	}

	for _, role := range []string{"manager", "admin", "super_admin"} {
		mgr := auth.User{ID: "u-" + role, Role: role}
		for _, path := range fragmentRoutes {
			if rec := get(path, mgr); rec.Code == http.StatusForbidden {
				t.Errorf("%s GET %s = 403, want past the catalog_management gate: %s", role, path, rec.Body.String())
			}
		}
	}
}

// No session at all must be refused on the GET fragment routes too, same
// contract as the mutating routes' TestCatalogHandlers_CatalogManagementGate_NoSessionDenied.
func TestCatalogHandlers_CatalogManagementGate_GETFragmentRoutesNoSessionDenied(t *testing.T) {
	t.Setenv("UT_AUTH", "on")
	mux, _ := newCatalogMuxRealSession(t)
	req := httptest.NewRequest(http.MethodGet, "/api/catalog/item-variants?item_id=itm1", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("no-session GET /api/catalog/item-variants = %d, want 403: %s", rec.Code, rec.Body.String())
	}
}

// No session at all must be refused on the page routes too, matching
// TestCatalogHandlers_CatalogManagementGate_NoSessionDenied's API-route
// contract.
func TestCatalogHandlers_CatalogManagementGate_PageRoutesNoSessionDenied(t *testing.T) {
	t.Setenv("UT_AUTH", "on")
	mux, _ := newCatalogMuxRealSession(t)
	for _, path := range []string{"/catalog", "/modifiers", "/catalog/option-sets"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("no-session GET %s = %d, want 403: %s", path, rec.Code, rec.Body.String())
		}
	}
}
