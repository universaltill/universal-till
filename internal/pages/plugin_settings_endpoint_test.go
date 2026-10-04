package pages

import (
	"context"
	"fmt"
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

// ADR-0121 §2 (ut-docs#3328): a setting declared `type: "endpoint"` only
// accepts an operator-entered http(s)://host[:port][/path]. The save is
// refused as a whole BEFORE any write, so a sibling setting in the same
// form is never half-saved.
func TestPluginSettingsAPI_POST_EndpointSettingValidated(t *testing.T) {
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
		"settings":[{"key":"erp_url","type":"endpoint"},{"key":"store_label"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	seedPluginSetting(t, dp, "p1", "erp_url", "https://erp.lan/api", "global")
	seedPluginSetting(t, dp, "p1", "store_label", "old", "global")

	stored := func(key string) string {
		t.Helper()
		got, found, err := data.NewPluginRepo(dp.Db).GetPluginSetting(ctx, "p1", key)
		if err != nil || !found {
			t.Fatalf("GetPluginSetting(%s) = %q %v %v", key, got, found, err)
		}
		return got
	}
	post := func(erpURL, label string) *httptest.ResponseRecorder {
		form := url.Values{"setting_erp_url": {erpURL}, "setting_store_label": {label}}
		req := httptest.NewRequest(http.MethodPost, "/api/plugins/p1/settings", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	wantMsg := fmt.Sprintf(httpx.T("en", "plugins.settings.invalid_endpoint"), "erp_url")
	if strings.HasPrefix(wantMsg, "plugins.settings.") {
		t.Fatalf("missing en.json key plugins.settings.invalid_endpoint")
	}
	for _, bad := range []string{"ftp://x", "not a url", "javascript:alert(1)"} {
		rec := post(bad, "new")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%q: code %d, want 400 (body %q)", bad, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), wantMsg) {
			t.Fatalf("%q: body %q, want the localized message %q", bad, rec.Body.String(), wantMsg)
		}
		if got := stored("erp_url"); got != `"https://erp.lan/api"` {
			t.Fatalf("%q: endpoint stored %s despite refusal", bad, got)
		}
		if got := stored("store_label"); got != `"old"` {
			t.Fatalf("%q: sibling setting half-saved as %s", bad, got)
		}
	}

	rec := post("https://10.0.0.5:8080/path", "new")
	if rec.Code != http.StatusOK {
		t.Fatalf("valid endpoint: code %d body %q", rec.Code, rec.Body.String())
	}
	if got := stored("erp_url"); got != `"https://10.0.0.5:8080/path"` {
		t.Fatalf("endpoint stored %s", got)
	}
	if got := stored("store_label"); got != `"new"` {
		t.Fatalf("sibling stored %s", got)
	}

	// Clearing the endpoint (blank) is allowed: it unsets the setting.
	rec = post("", "new")
	if rec.Code != http.StatusOK {
		t.Fatalf("blank endpoint: code %d body %q", rec.Code, rec.Body.String())
	}
	if got := stored("erp_url"); got != `""` {
		t.Fatalf("blank endpoint stored %s, want cleared", got)
	}
}
