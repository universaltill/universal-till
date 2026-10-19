package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/plugins"
)

// ut-docs#4135: a device check opened /plugins/integration-ai/settings —
// the AI Assistant's id is com.universaltill.integration-ai — and the page
// answered "This plugin has no settings" and "declares no permissions",
// which read as a broken install. A plugin that isn't installed has no
// settings page: go back to the Plugins list, as without permission.
func TestPluginSettingsPage_GET_NotInstalledRedirectsToPlugins(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newPluginSettingsTestDeps(t)

	req := httptest.NewRequest(http.MethodGet, "/plugins/integration-ai/settings", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("GET a not-installed plugin's settings: code %d, want 303; body %s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/plugins" {
		t.Fatalf("redirect to %q, want /plugins", loc)
	}
}

// The save endpoint refuses a not-installed plugin too: nothing written,
// no plugin_settings_saved audit row.
func TestPluginSettingsAPI_POST_NotInstalledIs404AndWritesNothing(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newPluginSettingsTestDeps(t)

	req := httptest.NewRequest(http.MethodPost, "/api/plugins/integration-ai/settings", strings.NewReader("setting_image_endpoint=http://10.0.0.5:7000"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true") // the page posts with htmx (ut-docs#4157: a no-JS save redirects)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("POST a not-installed plugin's settings: code %d, want 404; body %s", rec.Code, rec.Body.String())
	}
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'plugin_settings_saved'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("audit rows for a not-installed plugin's save = %d, want 0", n)
	}
}

// A disabled plugin is still installed: its settings page still opens.
func TestPluginSettingsPage_GET_DisabledPluginStillOpens(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newPluginSettingsTestDeps(t)
	if err := data.NewPluginRepo(dp.Db).SetPluginActive(context.Background(), nil, "p1", false); err != nil {
		t.Fatalf("disable p1: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/plugins/p1/settings", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET a disabled plugin's settings: code %d, want 200; body %s", rec.Code, rec.Body.String())
	}
}

// AI-Assistant-shaped manifests (the 2.3 → 2.4 settings growth): before
// and after an update, the settings page lists every declared setting and
// every declared permission (ut-docs#4135 AC 1, the update path).
const aiManifestV1 = `{"id":"com.universaltill.integration-ai","name":"AI Assistant","version":"2.3.1",
 "runtime":"none","entrypoint":"./x",
 "permissions":["events:receive","view:sales","net:@setting:endpoint","http:lan"],
 "settings":[
  {"key":"provider","default_value":"self_hosted","scope":"global"},
  {"key":"endpoint","default_value":"http://localhost:11434","scope":"global"},
  {"key":"vision_model","default_value":"llama3.2-vision","scope":"global"},
  {"key":"ask_model","default_value":"llama3.2","scope":"global"},
  {"key":"api_key","default_value":"","scope":"global","type":"secret"}]}`

const aiManifestV2 = `{"id":"com.universaltill.integration-ai","name":"AI Assistant","version":"2.4.0",
 "runtime":"none","entrypoint":"./x",
 "permissions":["events:receive","view:sales","view:inventory","net:@setting:endpoint","http:lan","net:api.openai.com"],
 "settings":[
  {"key":"provider","default_value":"self_hosted","scope":"global","type":"choice",
   "options":[{"value":"self_hosted","label":"Self-hosted"},{"value":"openai","label":"OpenAI"},{"value":"claude","label":"Claude"},{"value":"openai_compatible","label":"Compatible"}]},
  {"key":"endpoint","default_value":"http://localhost:11434","scope":"global","type":"endpoint",
   "show_when":{"key":"provider","in":["self_hosted","openai_compatible"]}},
  {"key":"vision_model","default_value":"llama3.2-vision","scope":"global"},
  {"key":"ask_model","default_value":"llama3.2","scope":"global"},
  {"key":"api_key","default_value":"","scope":"global","type":"secret",
   "show_when":{"key":"provider","in":["openai","claude","openai_compatible"]}},
  {"key":"image_provider","default_value":"self_hosted","scope":"global"},
  {"key":"image_endpoint","default_value":"","scope":"global"},
  {"key":"image_model","default_value":"birefnet-general-lite","scope":"global"}]}`

func TestPluginSettingsPage_GET_ListsEveryDeclaredSettingAndPermissionAcrossUpdate(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newPluginSettingsTestDeps(t)
	pluginsRoot := t.TempDir()
	prev := paths.DataDir()
	paths.Init(pluginsRoot)
	t.Cleanup(func() { paths.Init(prev) })

	for i, raw := range []string{aiManifestV1, aiManifestV2} {
		m, err := plugins.ParseManifest(strings.NewReader(raw))
		if err != nil {
			t.Fatalf("parse manifest %d: %v", i, err)
		}
		if err := plugins.PersistManifest(context.Background(), dp.Db, m, plugins.InstallOptions{}); err != nil {
			t.Fatalf("persist %s: %v", m.Version, err)
		}
		dir := paths.Plugins(m.ID, m.Version)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dir+"/manifest.json", []byte(raw), 0o644); err != nil {
			t.Fatal(err)
		}

		req := httptest.NewRequest(http.MethodGet, "/plugins/"+m.ID+"/settings", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("v%s: GET settings code %d body %s", m.Version, rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		for _, s := range m.Settings {
			if !strings.Contains(body, `name="setting_`+s.Key+`"`) {
				t.Errorf("v%s: setting %q missing from the settings page", m.Version, s.Key)
			}
		}
		for _, p := range m.Permissions {
			if !strings.Contains(body, `data-perm-name="`+p+`"`) {
				t.Errorf("v%s: permission %q missing from the settings page", m.Version, p)
			}
		}
	}
}
