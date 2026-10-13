package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/plugins"
)

func initHandlerForGuard(t *testing.T, cfg *config.Config) http.Handler {
	t.Helper()
	chdirRoot(t)
	paths.Init(t.TempDir())
	d := &db.DB{DB: openPagesTestDB(t)}
	t.Cleanup(func() { _ = d.Close() })
	cfg.Theme = "default"
	cfg.Locales = config.Locales{Currency: "GBP", TaxRateBP: 2000}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	pm, err := plugins.Init(ctx, cfg, d.DB)
	if err != nil {
		t.Fatalf("plugins.Init: %v", err)
	}
	var wg sync.WaitGroup
	h, _ := Init(ctx, ctx, cfg, pm, d.DB, nil, &wg)
	t.Cleanup(func() { httpx.InitRailVisibility(nil) })
	return h
}

// guardCode is the status a request from peer gets; a refusal by the guard
// itself (bare "Forbidden" text, not a handler's own 403 — exit-to-os answers
// 403 to a PIN-less POST by itself) is reported as guardRefused.
func guardCode(h http.Handler, peer, method, path string) int {
	r := httptest.NewRequest(method, path, nil)
	r.RemoteAddr = peer
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code == http.StatusForbidden && rec.Body.String() == http.StatusText(http.StatusForbidden)+"\n" {
		return guardRefused
	}
	return rec.Code
}

const guardRefused = -403

// ADR-0157 (ut-docs#4261), through the real Init chain: a phone on the shop
// Wi-Fi gets 403 for the PIN screen, the login API and exit-to-os, while
// till-to-till sync and the till's own screen work as before. Pinned with
// auth on and with UT_AUTH=off, since both branches of Init wrap the guard.
func TestInit_LANCallerGetsOnlySyncSurface(t *testing.T) {
	for _, authOff := range []bool{false, true} {
		name := "auth-on"
		if authOff {
			name = "auth-off"
			t.Setenv("UT_AUTH", "off")
		}
		t.Run(name, func(t *testing.T) {
			h := initHandlerForGuard(t, &config.Config{AuthDisabled: authOff})
			const lan = "192.168.1.50:50000"
			for _, c := range []struct{ method, path string }{
				{http.MethodGet, "/login"}, {http.MethodGet, "/"}, {http.MethodPost, "/api/auth/login"},
				{http.MethodPost, "/api/settings/exit-to-os"}, {http.MethodGet, "/setup"},
				{http.MethodGet, "/api/window-mode"},
			} {
				if got := guardCode(h, lan, c.method, c.path); got != guardRefused {
					t.Errorf("LAN %s %s = %d, want the guard's 403", c.method, c.path, got)
				}
			}
			// Reaches its handler (which does its own bearer check), not the guard.
			for _, p := range []string{"/api/sync/ping", "/healthz"} {
				if got := guardCode(h, lan, http.MethodGet, p); got == guardRefused {
					t.Errorf("LAN GET %s = 403, want it to reach its handler", p)
				}
			}
			if got := guardCode(h, "127.0.0.1:50000", http.MethodGet, "/login"); got == guardRefused {
				t.Errorf("loopback GET /login = 403, want the till's own screen served")
			}
		})
	}
}

// UT_UI_ALLOW_CIDRS opens the UI to a named network; an invalid list is
// dropped whole, never widening the guard.
func TestInit_UIAllowCIDRs(t *testing.T) {
	h := initHandlerForGuard(t, &config.Config{UIAllowCIDRs: "172.17.0.0/16"})
	if got := guardCode(h, "172.17.0.1:50000", http.MethodGet, "/login"); got == guardRefused {
		t.Errorf("allowed bridge GET /login = 403")
	}
	if got := guardCode(h, "192.168.1.50:50000", http.MethodGet, "/login"); got != guardRefused {
		t.Errorf("LAN GET /login = %d, want 403", got)
	}

	h = initHandlerForGuard(t, &config.Config{UIAllowCIDRs: "172.17.0.0/16,0.0.0.0/0"})
	if got := guardCode(h, "172.17.0.1:50000", http.MethodGet, "/login"); got != guardRefused {
		t.Errorf("invalid list: bridge GET /login = %d, want 403 (the whole list dropped)", got)
	}
}
