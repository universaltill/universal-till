package pages

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/settings"
)

// newSyncPairingGateTestDeps wires every subsystem sharing the
// sync_management action (ut-docs#707): sync_api.go, discovery_api.go (via
// the shared managerGate — api_gates.go), pairing_api.go,
// pending_pairings.go, and pairing_join.go's /api/sync/pair-start|
// pair-status (NOT in #707's named file list, but they gate through the
// exact same managerGate function, so converting it necessarily converts
// them too -- see the 044 migration's comment).
func newSyncPairingGateTestDeps(t *testing.T) *common.Deps {
	t.Helper()
	chdirRoot(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db) // role_permissions must exist for AuthSvc.Can() to answer

	cfg := &config.Config{
		Theme:   "default",
		Locales: config.Locales{Currency: "GBP", TaxRate: 20},
		Marketplace: config.MarketplaceConfig{
			EndpointURL: "http://localhost:8081",
		},
	}
	pm, err := plugins.Init(t.Context(), cfg, db)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	return &common.Deps{
		Cfg:      cfg,
		Db:       db,
		State:    state,
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		Pm:       pm,
		Settings: settings.NewStore(db),
		AuthSvc:  auth.NewService(db),
	}
}

// Positive counterpart to each subsystem's own TestXxx_RequiresManager
// negative test (ut-docs#707 — canPerform()/Auth.Can() against the new
// sync_management action, not just the no-session short-circuit those
// tests exercise). A real cashier session must still be denied; a real
// manager session must get PAST the auth gate. Several of these endpoints
// then respond non-2xx for unrelated reasons under this minimal fixture
// (unknown till id, no active pairing attempt) -- this only asserts the
// response isn't the auth gate's own denial code, same scope as
// ut-docs#706's TestPluginManagementEndpoints_RealSessionGatesByRole.
func TestSyncManagementEndpoints_RealSessionGatesByRole(t *testing.T) {
	dp := newSyncPairingGateTestDeps(t)
	stubBrowse(t, nil, nil) // discover-primaries: skip the real multi-second LAN scan

	mux := http.NewServeMux()
	tokens := registerSyncAPI(mux, dp)
	registerPairingAPI(mux, dp, dp.AuthSvc, tokens)
	registerPendingPairingsUI(mux, dp)
	registerDiscoveryAPI(mux, dp)
	registerPairingJoinAPI(mux, dp)

	cases := []struct {
		name, method, path string
		denyCode           int // the gate's own refusal status; StatusForbidden unless noted
	}{
		{"tills page", http.MethodGet, "/tills", http.StatusSeeOther}, // deny = redirect, not 403
		{"enroll-token", http.MethodPost, "/api/sync/enroll-token", http.StatusForbidden},
		{"tills revoke", http.MethodPost, "/api/sync/tills/some-till/revoke", http.StatusForbidden},
		{"promote", http.MethodPost, "/api/sync/promote", http.StatusForbidden},
		{"join", http.MethodPost, "/api/sync/join", http.StatusForbidden},
		{"discover-primaries", http.MethodGet, "/api/sync/discover-primaries", http.StatusForbidden},
		{"pair-requests", http.MethodGet, "/api/sync/pair-requests", http.StatusForbidden},
		{"pending-pairings ui", http.MethodGet, "/ui/tills/pending-pairings", http.StatusForbidden},
		{"pair-start", http.MethodPost, "/api/sync/pair-start", http.StatusForbidden},
		{"pair-status", http.MethodGet, "/api/sync/pair-status", http.StatusForbidden},
	}

	for _, tc := range cases {
		t.Run(tc.name+"/cashier_denied", func(t *testing.T) {
			req := auth.WithUser(httptest.NewRequest(tc.method, tc.path, nil), auth.User{ID: "u1", Role: "cashier"})
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != tc.denyCode {
				t.Fatalf("%s %s cashier = %d, want %d", tc.method, tc.path, rec.Code, tc.denyCode)
			}
		})
		for _, role := range []string{"manager", "admin", "super_admin"} {
			t.Run(tc.name+"/"+role+"_past_gate", func(t *testing.T) {
				req := auth.WithUser(httptest.NewRequest(tc.method, tc.path, nil), auth.User{ID: "u1", Role: role})
				rec := httptest.NewRecorder()
				mux.ServeHTTP(rec, req)
				if rec.Code == tc.denyCode {
					t.Fatalf("%s %s %s = %d (the gate's own denial code), want past the auth gate", tc.method, tc.path, role, rec.Code)
				}
			})
		}
	}
}
