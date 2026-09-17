package pages

// ut-docs#2224 / ADR-0098: versioned static assets are immutable. Every
// asset tag in base.html carries `?v={{ assetv … }}` (content-versioned),
// so a request that names a version can be cached for a year without a
// revalidation round trip — a changed file gets a new URL. The same path
// without a version stays `no-cache` (revalidate, today's effective
// behaviour), and HTML is never touched by either.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

const immutableCacheControl = "public, max-age=31536000, immutable"

func TestStaticAssets_VersionedRequestsAreImmutable(t *testing.T) {
	chdirRoot(t)
	mux := http.NewServeMux()
	registerStatic(mux)

	cases := []struct {
		path string
		want string
	}{
		{"/public/app.css?v=abc123", immutableCacheControl},
		{"/public/vendor/htmx.min.js?v=abc123", immutableCacheControl},
		{"/public/app.css", "no-cache"},
		{"/public/assets/logo/ut-logo.ico", "no-cache"},
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", c.path, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status %d", c.path, w.Code)
		}
		if got := w.Header().Get("Cache-Control"); got != c.want {
			t.Errorf("%s: Cache-Control = %q, want %q", c.path, got, c.want)
		}
	}
}

func TestStaticAssets_MissingFileGetsNoImmutableHeader(t *testing.T) {
	chdirRoot(t)
	mux := http.NewServeMux()
	registerStatic(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/public/does-not-exist.css?v=abc123", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", w.Code)
	}
	// A 404 body must never be cached for a year under an asset URL — the
	// file may appear later (a disk override, a self-update).
	if got := w.Header().Get("Cache-Control"); got == immutableCacheControl {
		t.Errorf("404 carried the immutable Cache-Control")
	}
}

func TestThemes_VersionedRequestsAreImmutable(t *testing.T) {
	d, _, pluginBase := themeTestDeps(t)
	mux := http.NewServeMux()
	registerThemes(mux, d)

	for _, c := range []struct{ path, want string }{
		{"/themes/dark.css?v=abc123", immutableCacheControl},
		{"/themes/dark.css", "no-cache"},
	} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", c.path, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status %d", c.path, w.Code)
		}
		if got := w.Header().Get("Cache-Control"); got != c.want {
			t.Errorf("%s: Cache-Control = %q, want %q", c.path, got, c.want)
		}
	}
	// A plugin theme can change without a new URL (plugin update, no
	// restart) — it keeps revalidating even when versioned.
	if _, err := d.Db.Exec(`INSERT INTO plugins(id,name,version,is_active) VALUES('com.x.midnight','Midnight','1.0.0',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Db.Exec(`INSERT INTO plugin_entries(id,plugin_id,type,key,label,config_json,is_active)
		VALUES('e1','com.x.midnight','theme','midnight','Midnight','{"css":"assets/theme.css"}',1)`); err != nil {
		t.Fatal(err)
	}
	cssPath := filepath.Join(pluginBase, "com.x.midnight", "1.0.0", "assets", "theme.css")
	if err := os.MkdirAll(filepath.Dir(cssPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cssPath, []byte("body{background:#0f172a}"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/themes/midnight.css?v=abc123", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("plugin theme: status %d", w.Code)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("plugin theme: Cache-Control = %q, want no-cache", got)
	}

	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/themes/nope.css?v=abc123", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing theme: status %d, want 404", w.Code)
	}
	if got := w.Header().Get("Cache-Control"); got == immutableCacheControl {
		t.Errorf("missing theme 404 carried the immutable Cache-Control")
	}
}
