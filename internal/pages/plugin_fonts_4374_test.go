package pages

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/plugins"
)

// ADR-0160 §8 / ut-docs#4374: a language bundle's fonts/*.woff2 are served
// from the till's own disk, and only the files the plugin manager listed
// (active language plugins) are reachable.

func pluginFontsMux(t *testing.T, fonts []plugins.BundleFont) (*http.ServeMux, string) {
	t.Helper()
	dir := t.TempDir()
	orig := pluginFontsDir
	pluginFontsDir = dir
	t.Cleanup(func() { pluginFontsDir = orig })
	mux := http.NewServeMux()
	registerPluginFontRoutes(mux, func() []plugins.BundleFont { return fonts })
	return mux, dir
}

func getFont(mux *http.ServeMux, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec
}

var thaiFont = plugins.BundleFont{PluginID: "com.universaltill.language-th", Version: "2.0.0", File: "NotoSansThai.woff2"}

func TestPluginFonts_ServesListedFont_4374(t *testing.T) {
	mux, dir := pluginFontsMux(t, []plugins.BundleFont{thaiFont})
	writePluginFile(t, dir, "com.universaltill.language-th/2.0.0/fonts/NotoSansThai.woff2", []byte("wOF2-bytes"))

	rec := getFont(mux, "/plugin-fonts/com.universaltill.language-th/2.0.0/NotoSansThai.woff2")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "wOF2-bytes" {
		t.Errorf("body = %q", rec.Body.String())
	}
	h := rec.Header()
	if got := h.Get("Content-Type"); got != "font/woff2" {
		t.Errorf("Content-Type = %q, want font/woff2", got)
	}
	if got := h.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q", got)
	}
	if got := h.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Access-Control-Allow-Origin = %q, want * (plugin page frames have an opaque origin)", got)
	}
	if got := h.Get("Content-Security-Policy"); !strings.Contains(got, "sandbox") || !strings.Contains(got, "default-src 'none'") {
		t.Errorf("Content-Security-Policy = %q", got)
	}
}

func TestPluginFonts_RefusesAnythingNotListed_4374(t *testing.T) {
	mux, dir := pluginFontsMux(t, []plugins.BundleFont{thaiFont})
	// On disk but not listed (another plugin, an inactive version, a
	// non-font file in the same bundle): never served.
	writePluginFile(t, dir, "com.universaltill.language-th/2.0.0/fonts/Other.woff2", []byte("x"))
	writePluginFile(t, dir, "com.universaltill.language-th/1.0.0/fonts/NotoSansThai.woff2", []byte("x"))
	writePluginFile(t, dir, "com.universaltill.language-th/2.0.0/locales/th.json", []byte("{}"))
	writePluginFile(t, dir, "com.x.theme/1.0.0/fonts/NotoSansThai.woff2", []byte("x"))

	for _, p := range []string{
		"/plugin-fonts/com.universaltill.language-th/2.0.0/Other.woff2",
		"/plugin-fonts/com.universaltill.language-th/1.0.0/NotoSansThai.woff2",
		"/plugin-fonts/com.universaltill.language-th/2.0.0/th.json",
		"/plugin-fonts/com.x.theme/1.0.0/NotoSansThai.woff2",
		"/plugin-fonts/com.universaltill.language-th/2.0.0/..%2Flocales%2Fth.json",
	} {
		if rec := getFont(mux, p); rec.Code != http.StatusNotFound {
			t.Errorf("%s: want 404, got %d", p, rec.Code)
		}
	}
}

func TestPluginFonts_ListedButMissingOnDisk_404_4374(t *testing.T) {
	mux, _ := pluginFontsMux(t, []plugins.BundleFont{thaiFont})
	if rec := getFont(mux, "/plugin-fonts/com.universaltill.language-th/2.0.0/NotoSansThai.woff2"); rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
}

func TestPluginFontsCSS_DeclaresEveryFontAndTheStackVariable_4374(t *testing.T) {
	ja := plugins.BundleFont{PluginID: "com.universaltill.language-ja", Version: "1.0.0", File: "NotoSansJP.woff2"}
	mux, _ := pluginFontsMux(t, []plugins.BundleFont{ja, thaiFont})

	rec := getFont(mux, "/plugin-fonts/bundle-fonts.css")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/css; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache (the set changes on install/enable)", got)
	}
	css := rec.Body.String()
	for _, want := range []string{
		`@font-face{font-family:"ut-bundle-1";src:url("/plugin-fonts/com.universaltill.language-ja/1.0.0/NotoSansJP.woff2") format("woff2");font-display:swap}`,
		`@font-face{font-family:"ut-bundle-2";src:url("/plugin-fonts/com.universaltill.language-th/2.0.0/NotoSansThai.woff2") format("woff2");font-display:swap}`,
		`:root{--ut-bundle-fonts:"ut-bundle-1","ut-bundle-2"}`,
	} {
		if !strings.Contains(css, want) {
			t.Errorf("CSS missing %s\n--- got ---\n%s", want, css)
		}
	}
	// A body that changes must change its validator, so a no-cache
	// revalidation picks up a newly installed bundle.
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}
	req := httptest.NewRequest("GET", "/plugin-fonts/bundle-fonts.css", nil)
	req.Header.Set("If-None-Match", etag)
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusNotModified {
		t.Errorf("same set, If-None-Match: want 304, got %d", rec2.Code)
	}
}

func TestPluginFontsCSS_NoFontsLeavesTheStackAlone_4374(t *testing.T) {
	mux, _ := pluginFontsMux(t, nil)
	rec := getFont(mux, "/plugin-fonts/bundle-fonts.css")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	// No --ut-bundle-fonts declaration at all: app.css's var() fallback
	// keeps the plain system stack.
	if strings.Contains(rec.Body.String(), "--ut-bundle-fonts") || strings.Contains(rec.Body.String(), "@font-face") {
		t.Errorf("empty set must declare nothing, got %q", rec.Body.String())
	}
}

// app.css must actually consume the variable, AFTER the generic sans-serif
// family: a bundle font then only draws glyphs the system fonts lack, so a
// language plugin can never restyle Latin text or digits (prices).
func TestAppCSS_BodyStackUsesBundleFontsAfterGeneric_4374(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("web", "public", "app.css"))
	if err != nil {
		t.Fatal(err)
	}
	css := string(raw)
	i := strings.Index(css, "\nbody {")
	if i < 0 {
		t.Fatal("no body rule in app.css")
	}
	rule := css[i : i+strings.Index(css[i:], "}")]
	g := strings.Index(rule, "sans-serif")
	v := strings.Index(rule, "var(--ut-bundle-fonts")
	if g < 0 || v < 0 || v < g {
		t.Fatalf("body font-family must list var(--ut-bundle-fonts, …) after sans-serif:\n%s", rule)
	}
}

// Every page that loads app.css also loads the bundle-font stylesheet, so a
// script a bundle adds renders on login, setup and the self-order kiosk too.
func TestTemplates_LoadBundleFontsWhereverAppCSSLoads_4374(t *testing.T) {
	root := filepath.Join("web", "ui")
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(p) != ".html" {
			return err
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		s := string(raw)
		if strings.Contains(s, `href="/public/app.css`) && !strings.Contains(s, `href="/plugin-fonts/bundle-fonts.css"`) {
			t.Errorf("%s loads app.css but not /plugin-fonts/bundle-fonts.css", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A plugin page renders inside an empty-sandbox srcdoc frame with its own
// <head>: it must load the bundle fonts too.
func TestPluginFrameDocument_LoadsBundleFonts_4374(t *testing.T) {
	doc, err := pluginFrameDocument("th", "", []byte("<p>x</p>"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc, `href="/plugin-fonts/bundle-fonts.css"`) {
		t.Fatalf("plugin frame document does not load the bundle fonts:\n%s", doc)
	}
}
