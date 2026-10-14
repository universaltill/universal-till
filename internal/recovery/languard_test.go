package recovery

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
	locales "github.com/universaltill/universal-till/web/locales"
)

// The recovery server binds the same all-interfaces address as the main
// server but builds its own mux, so it needs the ADR-0157 LAN guard itself
// (ut-docs#4268). 192.0.2.10 is TEST-NET-1: never one of this machine's own
// interface addresses, so it always counts as a LAN peer.
const (
	lanPeer      = "192.0.2.10:5555"
	loopbackPeer = "127.0.0.1:5555"
)

func guardedHandler(cfg *config.Config) (http.Handler, chan struct{}) {
	retry := make(chan struct{}, 1)
	failure := Failure{Kind: KindMigration, Detail: "exec migration 42: syntax error", RefCode: "AAAA-BBBB"}
	return newHandler(cfg, failure, false, nil, retry), retry
}

func serveFrom(h http.Handler, peer, method, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = peer
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func retryFired(retry chan struct{}) bool {
	select {
	case <-retry:
		return true
	default:
		return false
	}
}

func TestNewHandler_LANPeerCannotReachPageOrRetry(t *testing.T) {
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/"},
		{http.MethodPost, "/api/recovery/retry"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			h, retry := guardedHandler(&config.Config{})
			rec := serveFrom(h, lanPeer, tc.method, tc.path)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("LAN peer %s %s = %d, want 403", tc.method, tc.path, rec.Code)
			}
			if strings.Contains(rec.Body.String(), "AAAA-BBBB") {
				t.Fatalf("403 body leaks the ref code: %q", rec.Body.String())
			}
			if retryFired(retry) {
				t.Fatal("a refused LAN request still triggered a retry")
			}
		})
	}
}

func TestNewHandler_LANPeerStillReachesHealthz(t *testing.T) {
	h, _ := guardedHandler(&config.Config{})
	rec := serveFrom(h, lanPeer, http.MethodGet, "/healthz")
	if rec.Code == http.StatusForbidden {
		t.Fatalf("LAN peer GET /healthz = 403, want it through (shells poll it)")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /healthz = %d, want 503 (recovery mode is never healthy)", rec.Code)
	}
}

func TestNewHandler_LoopbackPeerReachesPage(t *testing.T) {
	h, _ := guardedHandler(&config.Config{})
	rec := serveFrom(h, loopbackPeer, http.MethodGet, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("loopback GET / = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "AAAA-BBBB") {
		t.Fatalf("recovery page missing the ref code:\n%s", rec.Body.String())
	}
}

func TestNewHandler_LoopbackPeerCanRetry(t *testing.T) {
	h, retry := guardedHandler(&config.Config{})
	rec := serveFrom(h, loopbackPeer, http.MethodPost, "/api/recovery/retry")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("loopback POST retry = %d, want 202", rec.Code)
	}
	if !retryFired(retry) {
		t.Fatal("loopback retry did not signal the retry channel")
	}
}

func TestNewHandler_AllowCIDRsOpensThePageToThatNetwork(t *testing.T) {
	h, _ := guardedHandler(&config.Config{UIAllowCIDRs: "192.0.2.0/24"})
	if rec := serveFrom(h, lanPeer, http.MethodGet, "/"); rec.Code != http.StatusOK {
		t.Fatalf("allowed-CIDR peer GET / = %d, want 200", rec.Code)
	}
	if rec := serveFrom(h, "198.51.100.7:5555", http.MethodGet, "/"); rec.Code != http.StatusForbidden {
		t.Fatalf("peer outside the allowed CIDR GET / = %d, want 403", rec.Code)
	}
}

func TestNewHandler_InvalidAllowCIDRsFailsClosed(t *testing.T) {
	h, _ := guardedHandler(&config.Config{UIAllowCIDRs: "not-a-cidr"})
	if rec := serveFrom(h, lanPeer, http.MethodGet, "/"); rec.Code != http.StatusForbidden {
		t.Fatalf("LAN peer with invalid UT_UI_ALLOW_CIDRS GET / = %d, want 403", rec.Code)
	}
	if rec := serveFrom(h, loopbackPeer, http.MethodGet, "/"); rec.Code != http.StatusOK {
		t.Fatalf("loopback peer with invalid UT_UI_ALLOW_CIDRS GET / = %d, want 200", rec.Code)
	}
}

func TestNewHandler_DemoModeStaysGuarded(t *testing.T) {
	h, retry := guardedHandler(&config.Config{Demo: true})
	if rec := serveFrom(h, lanPeer, http.MethodGet, "/"); rec.Code != http.StatusForbidden {
		t.Fatalf("demo-mode LAN peer GET / = %d, want 403", rec.Code)
	}
	if rec := serveFrom(h, lanPeer, http.MethodPost, "/api/recovery/retry"); rec.Code != http.StatusForbidden {
		t.Fatalf("demo-mode LAN peer POST retry = %d, want 403", rec.Code)
	}
	if retryFired(retry) {
		t.Fatal("demo-mode LAN retry was triggered")
	}
}

// safeModeHandler builds the handler with safe mode advertised as usable.
// A nil *sql.DB is fine: route registration only wraps it in a repo, and
// these tests never request the safe-mode routes themselves.
func safeModeHandler(cfg *config.Config) http.Handler {
	failure := Failure{Kind: KindMigration, Detail: "exec migration 42: syntax error", RefCode: "AAAA-BBBB"}
	return newHandler(cfg, failure, true, nil, make(chan struct{}, 1))
}

// withRealLocales wires the embedded locale files so the page renders
// English text rather than raw keys, and restores the key fallback after.
func withRealLocales(t *testing.T) {
	t.Helper()
	i18n, err := config.NewI18nFS(locales.FS, "en")
	if err != nil {
		t.Fatalf("load i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")
	t.Cleanup(func() { httpx.InitI18n(nil, "en") })
}

const safeModeThisDeviceOnly = "can only be opened on the till itself"

// #4290: the safe-mode routes are loopback-only, so a LAN peer the guard
// admits must not be offered a link that would 403.
func TestPage_AllowedLANPeerGetsNoteNotSafeModeLink(t *testing.T) {
	withRealLocales(t)
	h := safeModeHandler(&config.Config{UIAllowCIDRs: "192.0.2.0/24"})
	rec := serveFrom(h, lanPeer, http.MethodGet, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("allowed LAN peer GET / = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "/recovery/safe-mode") {
		t.Fatalf("LAN peer is offered the loopback-only safe-mode link:\n%s", body)
	}
	if !strings.Contains(body, safeModeThisDeviceOnly) {
		t.Fatalf("LAN peer page missing the this-device-only note:\n%s", body)
	}
}

func TestPage_LoopbackPeerGetsSafeModeLinkNotNote(t *testing.T) {
	withRealLocales(t)
	h := safeModeHandler(&config.Config{UIAllowCIDRs: "192.0.2.0/24"})
	rec := serveFrom(h, loopbackPeer, http.MethodGet, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("loopback GET / = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "/recovery/safe-mode") {
		t.Fatalf("loopback page missing the safe-mode link:\n%s", body)
	}
	if strings.Contains(body, safeModeThisDeviceOnly) {
		t.Fatalf("loopback page shows the this-device-only note:\n%s", body)
	}
}

func TestPage_SafeModeUnusableShowsNeitherLinkNorNote(t *testing.T) {
	withRealLocales(t)
	h, _ := guardedHandler(&config.Config{UIAllowCIDRs: "192.0.2.0/24"})
	for _, peer := range []string{lanPeer, loopbackPeer} {
		rec := serveFrom(h, peer, http.MethodGet, "/")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s GET / = %d, want 200", peer, rec.Code)
		}
		body := rec.Body.String()
		if strings.Contains(body, "/recovery/safe-mode") || strings.Contains(body, safeModeThisDeviceOnly) {
			t.Fatalf("%s: safe mode unusable but page mentions it:\n%s", peer, body)
		}
	}
}

func TestIsLoopbackPeer(t *testing.T) {
	for _, tc := range []struct {
		remote string
		want   bool
	}{
		{"127.0.0.1:5555", true},
		{"[::1]:5555", true},
		{"::1", true},
		{"192.0.2.10:5555", false},
		{"not-an-address", false},
		{"", false},
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = tc.remote
		r.Header.Set("X-Forwarded-For", "127.0.0.1")
		if got := isLoopbackPeer(r); got != tc.want {
			t.Errorf("isLoopbackPeer(%q) = %v, want %v", tc.remote, got, tc.want)
		}
	}
}
