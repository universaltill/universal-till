package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
)

// newExternalProxyDeps builds a Deps whose plugin manager exposes a single
// menu plugin pointing at the given upstream route.
func newExternalProxyDeps(t *testing.T, route string) *common.Deps {
	t.Helper()
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)
	pm, err := plugins.Init(t.Context(), &config.Config{}, db)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	pm.MenuPlugins = map[string]plugins.MenuPlugin{
		"good": {PluginID: "good", Key: "good", Route: route, Label: "Ext"},
		// A registered plugin with no route must not be proxied.
		"noroute": {PluginID: "noroute", Key: "noroute", Route: ""},
	}
	return &common.Deps{Db: db, Pm: pm}
}

func TestExternalProxy_ProxiesUpstreamBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<h1>hello from plugin</h1>"))
	}))
	defer upstream.Close()

	dp := newExternalProxyDeps(t, upstream.URL)
	mux := http.NewServeMux()
	registerExternalProxy(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/ext/good", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ext/good: code %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Fatalf("content-type = %q", ct)
	}
	if body := rec.Body.String(); body != "<h1>hello from plugin</h1>" {
		t.Fatalf("proxied body = %q", body)
	}
}

func TestExternalProxy_UnknownAndEmptyPlugin(t *testing.T) {
	dp := newExternalProxyDeps(t, "http://127.0.0.1:0")
	mux := http.NewServeMux()
	registerExternalProxy(mux, dp)

	cases := []string{
		"/ext/",        // empty plugin id
		"/ext/unknown", // not in MenuPlugins
		"/ext/noroute", // registered but has no route
	}
	for _, path := range cases {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("GET %s: code %d, want 404", path, rec.Code)
		}
	}
}

// TestExternalProxy_UpstreamHangRespectsRequestContext proves the proxy
// doesn't pin its goroutine on a hung upstream (ut-docs#912): a request
// carrying a short deadline must come back promptly once that deadline
// fires, rather than blocking for as long as the upstream stays open.
func TestExternalProxy_UpstreamHangRespectsRequestContext(t *testing.T) {
	block := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block // never responds until the test unblocks it
	}))
	// Deferred LIFO: unblock the handler before asking the server to Close,
	// which otherwise waits for the in-flight (blocked) handler forever.
	defer upstream.Close()
	defer close(block)

	dp := newExternalProxyDeps(t, upstream.URL)
	mux := http.NewServeMux()
	registerExternalProxy(mux, dp)

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/ext/good", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		mux.ServeHTTP(rec, req)
		close(done)
	}()

	select {
	case <-done:
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("GET /ext/good with hung upstream + expired context: code %d, want 502", rec.Code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("registerExternalProxy did not respect the request's context deadline — goroutine pinned on hung upstream")
	}
}

func TestExternalProxy_UpstreamDownIsBadGateway(t *testing.T) {
	// Port 1 is (practically) never listening, so the http.Get dial fails and
	// the proxy must surface a 502 rather than a 500 or a panic.
	dp := newExternalProxyDeps(t, "http://127.0.0.1:1")
	mux := http.NewServeMux()
	registerExternalProxy(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/ext/good", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("GET /ext/good with dead upstream: code %d, want 502", rec.Code)
	}
}

// ut-docs#924: a malformed plugin Route used to leak the raw url.Parse error
// via http.Error(w, err.Error(), ...) when http.NewRequestWithContext
// rejected it. Assert the localized fallback shows instead, never the raw
// Go error text.
func TestExternalProxy_MalformedRouteIsLocalized(t *testing.T) {
	// A NUL byte makes url.Parse (and so http.NewRequestWithContext) fail
	// with "net/url: invalid control character in URL".
	dp := newExternalProxyDeps(t, "http://example.invalid/\x00")
	mux := http.NewServeMux()
	registerExternalProxy(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/ext/good", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("malformed route = %d, want 502: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "unreachable") {
		t.Fatalf("malformed route error body = %q, want the localized unreachable message", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "control character") {
		t.Fatalf("malformed route error body leaked raw url.Parse error text: %q", rec.Body.String())
	}
}
