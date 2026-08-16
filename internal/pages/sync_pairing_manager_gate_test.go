package pages

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
		denyCode           int    // the gate's own refusal status; StatusForbidden unless noted
		body               string // request body (form-encoded); "" = none
	}{
		{name: "tills page", method: http.MethodGet, path: "/tills", denyCode: http.StatusSeeOther}, // deny = redirect, not 403
		{name: "enroll-token", method: http.MethodPost, path: "/api/sync/enroll-token", denyCode: http.StatusForbidden},
		{name: "tills revoke", method: http.MethodPost, path: "/api/sync/tills/some-till/revoke", denyCode: http.StatusForbidden},
		// ut-docs#557 review Fix 4: /api/sync/promote now validates its body
		// (confirm=="PROMOTE", and that this till IS actually a replica)
		// BEFORE ever checking elevation — so, unlike the other cases here,
		// this probe needs a body that clears that validation to actually
		// reach the auth gate at all (an empty body now 400s before that,
		// regardless of session).
		{name: "promote", method: http.MethodPost, path: "/api/sync/promote", denyCode: http.StatusForbidden, body: "confirm=PROMOTE"},
		{name: "join", method: http.MethodPost, path: "/api/sync/join", denyCode: http.StatusForbidden},
		{name: "discover-primaries", method: http.MethodGet, path: "/api/sync/discover-primaries", denyCode: http.StatusForbidden},
		{name: "pair-requests", method: http.MethodGet, path: "/api/sync/pair-requests", denyCode: http.StatusForbidden},
		{name: "pending-pairings ui", method: http.MethodGet, path: "/ui/tills/pending-pairings", denyCode: http.StatusForbidden},
		{name: "pair-start", method: http.MethodPost, path: "/api/sync/pair-start", denyCode: http.StatusForbidden},
		{name: "pair-status", method: http.MethodGet, path: "/api/sync/pair-status", denyCode: http.StatusForbidden},
	}

	for _, tc := range cases {
		if tc.name == "promote" {
			// Only /api/sync/promote's own validation needs this till to
			// actually BE a replica (its other half of Fix 4's body-first
			// validation) — seeded right before this one case runs so it
			// doesn't perturb any of the others above/below it in this loop.
			if err := dp.Settings.Set(t.Context(), "sync.primary_url", "http://primary.example"); err != nil {
				t.Fatalf("seed replica identity: %v", err)
			}
		}
		t.Run(tc.name+"/cashier_denied", func(t *testing.T) {
			var body io.Reader
			if tc.body != "" {
				body = strings.NewReader(tc.body)
			}
			req := auth.WithUser(httptest.NewRequest(tc.method, tc.path, body), auth.User{ID: "u1", Role: "cashier"})
			if tc.body != "" {
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			}
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			// ut-docs#557: POST /api/sync/promote alone moved off the flat
			// 403 onto checkOrElevate — a denied cashier now gets the
			// in-place elevation prompt (200) instead. Every other endpoint
			// above keeps its plain denyCode unchanged.
			if tc.name == "promote" {
				if rec.Code != http.StatusOK {
					t.Fatalf("%s %s cashier = %d, want 200 (elevation prompt)", tc.method, tc.path, rec.Code)
				}
				return
			}
			if rec.Code != tc.denyCode {
				t.Fatalf("%s %s cashier = %d, want %d", tc.method, tc.path, rec.Code, tc.denyCode)
			}
		})
		for _, role := range []string{"manager", "admin", "super_admin"} {
			t.Run(tc.name+"/"+role+"_past_gate", func(t *testing.T) {
				var body io.Reader
				if tc.body != "" {
					body = strings.NewReader(tc.body)
				}
				req := auth.WithUser(httptest.NewRequest(tc.method, tc.path, body), auth.User{ID: "u1", Role: role})
				if tc.body != "" {
					req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				}
				rec := httptest.NewRecorder()
				mux.ServeHTTP(rec, req)
				if rec.Code == tc.denyCode {
					t.Fatalf("%s %s %s = %d (the gate's own denial code), want past the auth gate", tc.method, tc.path, role, rec.Code)
				}
			})
		}
	}
}
