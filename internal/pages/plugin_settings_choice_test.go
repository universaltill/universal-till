package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/paths"
)

// ut-docs#2832: settings-form metadata on the plugin settings page — a
// `type: "choice"` setting renders as a dropdown, fields follow manifest
// order with their translated label, show_when hides fields that don't
// apply, and a saved secret says so.
func setupChoicePlugin(t *testing.T) (*http.ServeMux, func(key string) string) {
	t.Helper()
	return setupChoicePluginWithProvider(t, "hosted")
}

func setupChoicePluginWithProvider(t *testing.T, provider string) (*http.ServeMux, func(key string) string) {
	t.Helper()
	t.Setenv("UT_AUTH", "off")
	mux, dp := newPluginSettingsTestDeps(t)
	ctx := context.Background()
	pluginsRoot := t.TempDir()
	paths.Init(pluginsRoot)
	t.Cleanup(func() { paths.Init("") })
	version, ok, err := data.NewPluginRepo(dp.Db).GetActivePluginVersion(ctx, "p1")
	if err != nil || !ok {
		t.Fatalf("p1 active version: %q %v %v", version, ok, err)
	}
	manifestDir := paths.Plugins("p1", version)
	if err := os.MkdirAll(manifestDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "manifest.json"), []byte(`{"id":"p1","name":"Plugin","version":"`+version+`","runtime":"none",
		"settings":[
		  {"key":"provider","type":"choice","default_value":"local","label":"plugins.settings.save","help":"plugins.settings.none",
		   "options":[{"value":"local","label":"plugins.settings.permissions.granted"},{"value":"hosted","label":"plugins.settings.permissions.not_granted"}]},
		  {"key":"endpoint","show_when":{"key":"provider","in":["local"]}},
		  {"key":"api_key","type":"secret","show_when":{"key":"provider","in":["hosted"]}}
		]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Seeded in alphabetical order on purpose: the page must follow the
	// manifest (provider first), not the storage order.
	seedPluginSetting(t, dp, "p1", "api_key", "sk-saved", "global")
	seedPluginSetting(t, dp, "p1", "endpoint", "http://localhost:11434", "global")
	seedPluginSetting(t, dp, "p1", "provider", provider, "global")
	stored := func(key string) string {
		t.Helper()
		got, found, err := data.NewPluginRepo(dp.Db).GetPluginSetting(ctx, "p1", key)
		if err != nil || !found {
			t.Fatalf("GetPluginSetting(%s) = %q %v %v", key, got, found, err)
		}
		return got
	}
	return mux, stored
}

func getChoiceSettingsBody(t *testing.T, mux *http.ServeMux) string {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/plugins/p1/settings", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET: code %d body %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func TestPluginSettingsPage_ChoiceRendersDropdownInManifestOrder(t *testing.T) {
	mux, _ := setupChoicePlugin(t)
	body := getChoiceSettingsBody(t, mux)
	en := func(k string) string { return httpx.T("en", k) }

	if !strings.Contains(body, `<select name="setting_provider"`) {
		t.Fatalf("provider must render as a dropdown, got %s", body)
	}
	if strings.Contains(body, `type="text" name="setting_provider"`) {
		t.Fatal("provider must not also render as a text box")
	}
	if !strings.Contains(body, `<option value="hosted" selected>`+en("plugins.settings.permissions.not_granted")+`</option>`) {
		t.Fatalf("stored option must be selected with its translated label, got %s", body)
	}
	if !strings.Contains(body, `<option value="local">`+en("plugins.settings.permissions.granted")+`</option>`) {
		t.Fatalf("other option must render with its translated label, got %s", body)
	}
	if !strings.Contains(body, en("plugins.settings.save")) || !strings.Contains(body, en("plugins.settings.none")) {
		t.Fatal("the declared label and help must render translated")
	}
	iProvider := strings.Index(body, `name="setting_provider"`)
	iEndpoint := strings.Index(body, `name="setting_endpoint"`)
	iKey := strings.Index(body, `name="setting_api_key"`)
	if iProvider < 0 || iEndpoint < 0 || iKey < 0 || iProvider > iEndpoint || iEndpoint > iKey {
		t.Fatalf("fields must follow manifest order provider < endpoint < api_key, got %d %d %d", iProvider, iEndpoint, iKey)
	}
	// provider = hosted: endpoint hidden (server-side), api_key shown.
	if !strings.Contains(body, `data-show-when-key="provider" data-show-when-in="local" hidden`) {
		t.Fatalf("endpoint must be hidden while provider = hosted, got %s", body)
	}
	if !strings.Contains(body, `data-show-when-key="provider" data-show-when-in="hosted">`) {
		t.Fatalf("api_key must be shown (not hidden) while provider = hosted, got %s", body)
	}
	if strings.Contains(body, "sk-saved") {
		t.Fatal("a secret's value must never reach the page")
	}
	if !strings.Contains(body, en("plugins.settings.secret_saved")) || strings.HasPrefix(en("plugins.settings.secret_saved"), "plugins.") {
		t.Fatal("a saved secret must show the translated 'saved' tag")
	}
}

// A stored value that is not an option (an older install typed "OpenAI")
// stays selectable as itself and can be re-saved unchanged — the page never
// silently rewrites it — but a NEW non-option value is refused.
func TestPluginSettingsPage_ChoiceKeepsUnknownStoredValue(t *testing.T) {
	mux, stored := setupChoicePluginWithProvider(t, "OpenAI")
	body := getChoiceSettingsBody(t, mux)
	if !strings.Contains(body, `<option value="OpenAI" selected data-unknown>OpenAI</option>`) {
		t.Fatalf("legacy stored value must stay selected as itself, got %s", body)
	}
	if rec := postChoice(t, mux, url.Values{"setting_provider": {"OpenAI"}, "setting_endpoint": {"http://x:1"}}); rec.Code != http.StatusOK {
		t.Fatalf("re-saving the unchanged legacy value must be allowed, code %d body %q", rec.Code, rec.Body.String())
	}
	if got := stored("endpoint"); got != `"http://x:1"` {
		t.Fatalf("sibling not saved: %s", got)
	}
	if rec := postChoice(t, mux, url.Values{"setting_provider": {"Claude"}}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a new non-option value must be refused, code %d", rec.Code)
	}
}

func postChoice(t *testing.T, mux *http.ServeMux, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/plugins/p1/settings", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true") // the page posts with htmx (ut-docs#4157: a no-JS save redirects)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestPluginSettingsAPI_POST_ChoiceValidated(t *testing.T) {
	mux, stored := setupChoicePlugin(t)
	// ut-docs#4157: a refused choice is a 422 field error on the re-rendered form.
	wantMsg := httpx.T("en", "plugins.settings.error.choice")
	if strings.HasPrefix(wantMsg, "plugins.settings.") {
		t.Fatal("missing en.json key plugins.settings.error.choice")
	}
	for _, bad := range []string{"OpenAI", "claude", ""} {
		rec := postChoice(t, mux, url.Values{"setting_provider": {bad}, "setting_endpoint": {"http://changed:1"}})
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), wantMsg) {
			t.Fatalf("%q: code %d body %q, want 422 %q", bad, rec.Code, rec.Body.String(), wantMsg)
		}
		if got := stored("provider"); got != `"hosted"` {
			t.Fatalf("%q: provider stored %s despite refusal", bad, got)
		}
		if got := stored("endpoint"); got != `"http://localhost:11434"` {
			t.Fatalf("%q: sibling half-saved as %s", bad, got)
		}
	}
	if rec := postChoice(t, mux, url.Values{"setting_provider": {"local"}}); rec.Code != http.StatusOK {
		t.Fatalf("valid option: code %d body %q", rec.Code, rec.Body.String())
	}
	if got := stored("provider"); got != `"local"` {
		t.Fatalf("provider stored %s, want local", got)
	}
}

// Review finding 1: an empty stored choice must not render as option 1
// selected (saving a sibling would silently write option 1).
func TestPluginSettingsPage_ChoiceEmptyStoredValueStaysEmpty(t *testing.T) {
	mux, stored := setupChoicePluginWithProvider(t, "")
	body := getChoiceSettingsBody(t, mux)
	if !strings.Contains(body, `<option value="" selected>`+httpx.T("en", "plugins.settings.secret_empty")+`</option>`) {
		t.Fatalf("an empty stored choice must render a selected 'not set' entry, got %s", body)
	}
	if rec := postChoice(t, mux, url.Values{"setting_provider": {""}, "setting_endpoint": {"http://x:1"}}); rec.Code != http.StatusOK {
		t.Fatalf("re-saving the unchanged empty value must be allowed, code %d body %q", rec.Code, rec.Body.String())
	}
	if got := stored("provider"); got != `""` {
		t.Fatalf("provider rewritten to %s", got)
	}
}

// Review finding 6: a legacy non-option controlling value must not hide
// the fields that depend on it (the plugin may still use them).
func TestPluginSettingsPage_LegacyControllerShowsDependents(t *testing.T) {
	mux, _ := setupChoicePluginWithProvider(t, "OpenAI")
	body := getChoiceSettingsBody(t, mux)
	if strings.Contains(body, ` hidden>`) && strings.Contains(body, `data-show-when-key="provider" data-show-when-in="local" hidden`) {
		t.Fatalf("endpoint must stay visible under a legacy provider value, got %s", body)
	}
	if strings.Contains(body, `data-show-when-in="hosted" hidden`) {
		t.Fatal("api_key must stay visible under a legacy provider value")
	}
	if !strings.Contains(body, `<option value="OpenAI" selected data-unknown>OpenAI</option>`) {
		t.Fatalf("the legacy option must be marked for the live script, got %s", body)
	}
}

// Review finding 7: a stored legacy value with surrounding whitespace must
// still be re-savable unchanged.
func TestPluginSettingsAPI_POST_ChoiceLegacyWhitespace(t *testing.T) {
	mux, _ := setupChoicePluginWithProvider(t, " OpenAI ")
	if rec := postChoice(t, mux, url.Values{"setting_provider": {" OpenAI "}}); rec.Code != http.StatusOK {
		t.Fatalf("unchanged legacy value with whitespace refused: %d %q", rec.Code, rec.Body.String())
	}
}

// Owner 2026-10-10: a stored key must be removable, not only replaceable.
func TestPluginSettingsAPI_POST_RemoveSavedSecret(t *testing.T) {
	mux, stored := setupChoicePlugin(t)
	body := getChoiceSettingsBody(t, mux)
	if !strings.Contains(body, `name="clear_setting_api_key"`) {
		t.Fatalf("a saved secret must offer a remove checkbox, got %s", body)
	}
	if rec := postChoice(t, mux, url.Values{"setting_provider": {"hosted"}, "setting_api_key": {""}, "clear_setting_api_key": {"1"}}); rec.Code != http.StatusOK {
		t.Fatalf("remove: code %d body %q", rec.Code, rec.Body.String())
	}
	if got := stored("api_key"); got != `""` {
		t.Fatalf("api_key after remove = %s, want cleared", got)
	}
	// A blank submission without the checkbox still keeps the value.
	mux2, stored2 := setupChoicePlugin(t)
	if rec := postChoice(t, mux2, url.Values{"setting_api_key": {""}}); rec.Code != http.StatusOK {
		t.Fatalf("blank keep: %d", rec.Code)
	}
	if got := stored2("api_key"); got != `"sk-saved"` {
		t.Fatalf("blank submission cleared the key: %s", got)
	}
}
