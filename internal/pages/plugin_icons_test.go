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
