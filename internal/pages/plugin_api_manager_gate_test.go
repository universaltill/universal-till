package pages

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Every plugin-management endpoint that mutates install state (install,
// uninstall, enable, disable, update, rollback, permission grant/revoke,
// trust level, import) must reject a request with no manager session —
// previously none of them checked this at all. UT_AUTH is deliberately left
// unset (not "off") so isManagerOrAuthOff's bypass doesn't mask a missing
// gate. (The legacy unverified "upload"/"marketplace install" endpoints
// this table used to cover were removed outright, ut-docs#480 — see
// TestLegacyInstallEndpoints_Removed in plugin_api_legacy_test.go.)
func TestPluginManagementEndpoints_RejectWithoutManagerAuth(t *testing.T) {
	chdirRoot(t)
	db := openPagesTestDB(t)
	defer db.Close()

	dp := &common.Deps{Cfg: &config.Config{}, Db: db}
	mux := http.NewServeMux()
	registerPluginAPI(mux, dp)

	cases := []struct {
		name   string
		method string
		path   string
	}{
		{"permissions grant", http.MethodPost, "/api/plugins/permissions/grant"},
		{"permissions revoke", http.MethodPost, "/api/plugins/permissions/revoke"},
		{"trust level", http.MethodPost, "/api/plugins/trust"},
		{"install from marketplace", http.MethodPost, "/api/plugins/install-from-marketplace"},
		{"enable", http.MethodPost, "/api/plugins/some-plugin/enable"},
		{"disable", http.MethodPost, "/api/plugins/some-plugin/disable"},
		{"uninstall", http.MethodPost, "/api/plugins/some-plugin/uninstall"},
		{"update", http.MethodPost, "/api/plugins/some-plugin/update"},
		{"rollback", http.MethodPost, "/api/plugins/some-plugin/rollback"},
		{"import from file", http.MethodPost, "/api/plugins/import-from-file"},
		{"export", http.MethodGet, "/api/plugins/some-plugin/export"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s %s = %d, want 403 (no manager session, UT_AUTH not off)", tc.method, tc.path, rec.Code)
			}
		})
	}
}

// Positive counterpart to TestPluginManagementEndpoints_RejectWithoutManagerAuth
// (ut-docs#706 — canPerform()/Auth.Can(), not just the no-session
// short-circuit that test exercises). A real cashier session must still be
// denied by the actual permission check; a real manager session must get
// PAST the auth gate. Several of these endpoints then fail for unrelated
// reasons under this minimal fixture (no marketplace configured, plugin not
// found) — this only asserts the response isn't 403, which is the auth
// gate's own signal, same scope as the negative test above.
func TestPluginManagementEndpoints_RealSessionGatesByRole(t *testing.T) {
	chdirRoot(t)
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db) // role_permissions must exist for AuthSvc.Can() to answer

	dp := &common.Deps{Cfg: &config.Config{}, Db: db, AuthSvc: auth.NewService(db)}
	mux := http.NewServeMux()
	registerPluginAPI(mux, dp)

	cases := []struct {
		name   string
		method string
		path   string
	}{
		{"permissions grant", http.MethodPost, "/api/plugins/permissions/grant"},
		{"permissions revoke", http.MethodPost, "/api/plugins/permissions/revoke"},
		{"trust level", http.MethodPost, "/api/plugins/trust"},
		{"install from marketplace", http.MethodPost, "/api/plugins/install-from-marketplace"},
		{"enable", http.MethodPost, "/api/plugins/some-plugin/enable"},
		{"disable", http.MethodPost, "/api/plugins/some-plugin/disable"},
		{"uninstall", http.MethodPost, "/api/plugins/some-plugin/uninstall"},
		{"update", http.MethodPost, "/api/plugins/some-plugin/update"},
		{"rollback", http.MethodPost, "/api/plugins/some-plugin/rollback"},
		{"import from file", http.MethodPost, "/api/plugins/import-from-file"},
		{"export", http.MethodGet, "/api/plugins/some-plugin/export"},
	}

	for _, tc := range cases {
		t.Run(tc.name+"/cashier_denied", func(t *testing.T) {
			req := auth.WithUser(httptest.NewRequest(tc.method, tc.path, nil), auth.User{ID: "u1", Role: "cashier"})
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s %s cashier = %d, want 403", tc.method, tc.path, rec.Code)
			}
		})
		t.Run(tc.name+"/manager_past_gate", func(t *testing.T) {
			req := auth.WithUser(httptest.NewRequest(tc.method, tc.path, nil), auth.User{ID: "u1", Role: "manager"})
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code == http.StatusForbidden {
				t.Fatalf("%s %s manager = 403, want past the auth gate (fixture has no plugin/marketplace, so a non-403 failure downstream is fine)", tc.method, tc.path)
			}
		})
	}
}

// The plugin STORE lifecycle API (/api/plugins/store/*) is a separate,
// parallel route registration (registerPluginStoreAPI) from the main plugin
// management API above — a live install path that was missed in an earlier
// pass of this same fix (caught by independent review before merge) and
// left a cashier able to install/download/delete plugins via the store page
// despite the rest of this file being gated.
func TestPluginStoreEndpoints_RejectWithoutManagerAuth(t *testing.T) {
	chdirRoot(t)
	db := openPagesTestDB(t)
	defer db.Close()

	dp := &common.Deps{Cfg: &config.Config{}, Db: db}
	mux := http.NewServeMux()
	registerPluginStoreAPI(mux, dp)

	cases := []struct{ name, path string }{
		{"download", "/api/plugins/store/download"},
		{"install", "/api/plugins/store/install"},
		{"delete-download", "/api/plugins/store/delete-download"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.path, nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("POST %s = %d, want 403 (no manager session, UT_AUTH not off)", tc.path, rec.Code)
			}
		})
	}
}

// Positive counterpart to TestPluginStoreEndpoints_RejectWithoutManagerAuth
// (ut-docs#706 — real cashier session denied, real manager session past the
// gate; the store's own listing_id/marketplace-config checks fail these
// downstream under this minimal fixture, so only the non-403 signal is
// asserted for manager, same scope as the plugin-management table above).
func TestPluginStoreEndpoints_RealSessionGatesByRole(t *testing.T) {
	chdirRoot(t)
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db) // role_permissions must exist for AuthSvc.Can() to answer

	dp := &common.Deps{Cfg: &config.Config{}, Db: db, AuthSvc: auth.NewService(db)}
	mux := http.NewServeMux()
	registerPluginStoreAPI(mux, dp)

	cases := []struct{ name, path string }{
		{"download", "/api/plugins/store/download"},
		{"install", "/api/plugins/store/install"},
		{"delete-download", "/api/plugins/store/delete-download"},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/cashier_denied", func(t *testing.T) {
			req := auth.WithUser(httptest.NewRequest(http.MethodPost, tc.path, nil), auth.User{ID: "u1", Role: "cashier"})
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("POST %s cashier = %d, want 403", tc.path, rec.Code)
			}
		})
		t.Run(tc.name+"/manager_past_gate", func(t *testing.T) {
			req := auth.WithUser(httptest.NewRequest(http.MethodPost, tc.path, nil), auth.User{ID: "u1", Role: "manager"})
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code == http.StatusForbidden {
				t.Fatalf("POST %s manager = 403, want past the auth gate", tc.path)
			}
		})
	}
}

// The read-only catalog-browsing and update-check endpoints must NOT be
// manager-gated — a cashier browsing the marketplace or checking for
// updates is harmless and shouldn't need a manager PIN.
func TestPluginCatalogBrowsing_DoesNotRequireManagerAuth(t *testing.T) {
	chdirRoot(t)
	db := openPagesTestDB(t)
	defer db.Close()

	dp := &common.Deps{Cfg: &config.Config{}, Db: db}
	mux := http.NewServeMux()
	registerPluginAPI(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/api/plugins/marketplace", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code == http.StatusForbidden {
		t.Fatal("marketplace browsing must not require a manager session")
	}
}
