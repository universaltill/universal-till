package pages

import (
	"net/http"
	"net/http/httptest"
	"testing"

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
