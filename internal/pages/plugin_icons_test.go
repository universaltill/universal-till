package pages

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
)

func withPluginIconsDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	orig := pluginIconsDir
	pluginIconsDir = dir
	t.Cleanup(func() { pluginIconsDir = orig })
	return dir
}

func TestPluginIcons_ServesFileUnderPluginDir(t *testing.T) {
	base := withPluginIconsDir(t)
	iconPath := filepath.Join(base, "com.x.stripe", "1.0.0", "icons", "card.svg")
	if err := os.MkdirAll(filepath.Dir(iconPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(iconPath, []byte("<svg/>"), 0o644); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	registerPluginIcons(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/plugin-icons/com.x.stripe/1.0.0/icons/card.svg", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "<svg/>" {
		t.Errorf("unexpected body: %q", rec.Body.String())
	}
	assertIconHeaders(t, rec, "image/svg+xml")
}

func writePluginFile(t *testing.T, base, rel string, body []byte) {
	t.Helper()
	p := filepath.Join(base, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func getIcon(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	registerPluginIcons(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec
}

// assertIconHeaders: an explicit type from the allowlist, no sniffing, and
// a CSP that sandboxes the response even when opened top-level
// (ut-docs#2892 security review).
func assertIconHeaders(t *testing.T, rec *httptest.ResponseRecorder, wantType string) {
	t.Helper()
	if got := rec.Header().Get("Content-Type"); got != wantType {
		t.Errorf("Content-Type = %q, want %q", got, wantType)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	csp := rec.Header().Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'none'", "sandbox"} {
		if !strings.Contains(csp, want) {
			t.Errorf("Content-Security-Policy %q missing %q", csp, want)
		}
	}
}

// A plugin shipping an HTML (or any non-image) file must not get it served
// on the till origin through the auth-exempt icon route.
func TestPluginIcons_RefusesNonImageFiles(t *testing.T) {
	base := withPluginIconsDir(t)
	for _, rel := range []string{"docs/page.html", "content/index.html", "x.htm", "a.js", "manifest.json", "notes.txt", "x.xhtml", "icon.svgz", "noext"} {
		writePluginFile(t, base, filepath.Join("com.x.evil", "1.0.0", rel), []byte("<script>alert(1)</script>"))
		rec := getIcon(t, "/plugin-icons/com.x.evil/1.0.0/"+rel)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: want 404, got %d (%q)", rel, rec.Code, rec.Header().Get("Content-Type"))
		}
	}
}

// A scripted SVG is still an allowed icon type, but it is served sandboxed
// with no script-src, so opening it top-level cannot run script.
func TestPluginIcons_ScriptedSVGIsSandboxed(t *testing.T) {
	base := withPluginIconsDir(t)
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(document.cookie)</script></svg>`)
	writePluginFile(t, base, "com.x.evil/1.0.0/icons/evil.svg", svg)
	rec := getIcon(t, "/plugin-icons/com.x.evil/1.0.0/icons/evil.svg")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	assertIconHeaders(t, rec, "image/svg+xml")
}

// Real plugins ship PNG icons (ut-plugin-faq, layout-salon: assets/icon.png).
func TestPluginIcons_ServesAllowedRasterTypes(t *testing.T) {
	base := withPluginIconsDir(t)
	for rel, want := range map[string]string{
		"assets/icon.png": "image/png", "a.jpg": "image/jpeg", "a.JPEG": "image/jpeg",
		"a.webp": "image/webp", "a.gif": "image/gif", "a.ico": "image/x-icon",
	} {
		writePluginFile(t, base, filepath.Join("com.x.faq", "1.0.0", rel), []byte("\x89PNG<html><script>x</script>"))
		rec := getIcon(t, "/plugin-icons/com.x.faq/1.0.0/"+rel)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: want 200, got %d", rel, rec.Code)
			continue
		}
		assertIconHeaders(t, rec, want)
	}
}

// Each unsafe id/version below has a real file on disk, so only the
// segment validation can be what refuses it.
func TestPluginIcons_RejectsUnsafeIDAndVersion(t *testing.T) {
	base := withPluginIconsDir(t)
	for path, onDisk := range map[string]string{
		"/plugin-icons/.hidden/1.0.0/icon.png":     ".hidden/1.0.0/icon.png",
		"/plugin-icons/com%20x/1.0.0/icon.png":     "com x/1.0.0/icon.png",
		"/plugin-icons/com.x..faq/1.0.0/icon.png":  "com.x..faq/1.0.0/icon.png",
		"/plugin-icons/com.x.faq/1.0.0;x/icon.png": "com.x.faq/1.0.0;x/icon.png",
		"/plugin-icons/com.x.faq/_1.0.0/icon.png":  "com.x.faq/_1.0.0/icon.png",
	} {
		writePluginFile(t, base, onDisk, []byte("png"))
		if rec := getIcon(t, path); rec.Code == http.StatusOK {
			t.Errorf("%s: unsafe id/version must not be served", path)
		}
	}
	// ...while a well-formed pair is.
	writePluginFile(t, base, "com.x-faq_2/1.0.0-beta.1+b7/icon.png", []byte("png"))
	if rec := getIcon(t, "/plugin-icons/com.x-faq_2/1.0.0-beta.1+b7/icon.png"); rec.Code != http.StatusOK {
		t.Errorf("well-formed id/version refused: %d", rec.Code)
	}
}

func TestPluginIcons_BlocksPathTraversal(t *testing.T) {
	base := withPluginIconsDir(t)
	// A secret file that lives OUTSIDE any plugin's own directory — must
	// never be reachable through the icon route.
	secretDir := filepath.Join(base, "..", "outside")
	if err := os.MkdirAll(secretDir, 0o755); err != nil {
		t.Fatal(err)
	}
	secretPath := filepath.Join(secretDir, "secret.txt")
	if err := os.WriteFile(secretPath, []byte("top secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(secretDir) })

	mux := http.NewServeMux()
	registerPluginIcons(mux)

	for _, path := range []string{
		"/plugin-icons/com.x.stripe/1.0.0/../../../outside/secret.txt",
		"/plugin-icons/..%2f..%2foutside/1.0.0/secret.txt",
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", path, nil)
		mux.ServeHTTP(rec, req)
		if rec.Code == http.StatusOK {
			t.Errorf("traversal path %q must not be served, got 200: %s", path, rec.Body.String())
		}
	}
}

// A button entry with an icon renders an <img> pointing at the guarded
// route; a button with no icon renders no <img> at all.
func TestPluginButtonsTemplate_RendersIconOnlyWhenSet(t *testing.T) {
	chdirRoot(t)

	buttons := []data.ButtonEntryRow{
		{PluginID: "com.x.stripe", PluginVersion: "1.0.0", PluginName: "Stripe", EntryKey: "tender", Label: "Card", IconPath: "icons/card.svg"},
		{PluginID: "com.x.plain", PluginVersion: "2.0.0", PluginName: "Plain", EntryKey: "noop", Label: "No Icon"},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/ui/plugin-buttons", nil)
	httpx.RenderPartial("ui/partials/plugin_buttons.html", map[string]any{"Buttons": buttons})(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, `/plugin-icons/com.x.stripe/1.0.0/icons/card.svg`) {
		t.Error("expected the icon <img> src for the button with icon_path")
	}
	if strings.Count(body, "<img") != 1 {
		t.Errorf("expected exactly 1 <img> (only the button with an icon), got %d", strings.Count(body, "<img"))
	}
}

func TestPluginIcons_MissingFileIs404(t *testing.T) {
	withPluginIconsDir(t)

	mux := http.NewServeMux()
	registerPluginIcons(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/plugin-icons/com.x.nope/1.0.0/missing.svg", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
}
