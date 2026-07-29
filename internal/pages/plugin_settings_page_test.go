package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/settings"
)

func TestIsSecretSettingKey(t *testing.T) {
	secret := []string{
		"api_key", "apikey", "API_KEY", "my_secret_value", "token",
		"password", "passwd", "auth_value", "private_key", "webhook_key", "key",
	}
	for _, k := range secret {
		if !isSecretSettingKey(k) {
			t.Errorf("expected %q to be classified as secret", k)
		}
	}
	plain := []string{"endpoint_url", "model_name", "currency", "timeout_seconds", ""}
	for _, k := range plain {
		if isSecretSettingKey(k) {
			t.Errorf("expected %q to NOT be classified as secret", k)
		}
	}
}

func newPluginSettingsTestDeps(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	initPagesI18n(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)

	cfg := &config.Config{
		Theme:   "default",
		Locales: config.Locales{Currency: "GBP", Locale: "en", TaxRate: 20},
	}
	pm, err := plugins.Init(t.Context(), cfg, db)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	dp := &common.Deps{
		Cfg:      cfg,
		Db:       db,
		State:    state,
		Menu:     []common.MenuItem{{Href: "/plugins", Label: "Plugins"}},
		Pm:       pm,
		Settings: settings.NewStore(db),
	}
	mux := http.NewServeMux()
	registerPluginSettings(mux, dp)
	return mux, dp
}

func seedPluginSetting(t *testing.T, db *common.Deps, pluginID, key, value, scope string) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := data.NewPluginRepo(db.Db).UpsertPluginSettingScoped(context.Background(), pluginID, key, string(raw), scope); err != nil {
		t.Fatalf("seed plugin setting %s: %v", key, err)
	}
}

func TestPluginSettingsPage_GET_RequiresManager(t *testing.T) {
	mux, _ := newPluginSettingsTestDeps(t)
	req := httptest.NewRequest(http.MethodGet, "/plugins/p1/settings", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected a 303 redirect without a manager session, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/plugins" {
		t.Fatalf("expected redirect to /plugins, got %q", loc)
	}
}

func TestPluginSettingsPage_GET_RendersPlainValueAndMasksSecret(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newPluginSettingsTestDeps(t)
	seedPluginSetting(t, dp, "p1", "endpoint_url", "https://plugin.example/api", "global")
	seedPluginSetting(t, dp, "p1", "api_key", "sk-super-secret-value", "global")

	req := httptest.NewRequest(http.MethodGet, "/plugins/p1/settings", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /plugins/p1/settings: code %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "https://plugin.example/api") {
		t.Fatalf("expected the plain setting's value rendered in the page, got %q", body)
	}
	if strings.Contains(body, "sk-super-secret-value") {
		t.Fatalf("a secret setting's actual value must never be sent to the page, got %q", body)
	}
}

func TestPluginSettingsAPI_POST_RequiresManager(t *testing.T) {
	mux, _ := newPluginSettingsTestDeps(t)
	req := httptest.NewRequest(http.MethodPost, "/api/plugins/p1/settings", strings.NewReader("setting_endpoint_url=x"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without a manager session, got %d", rec.Code)
	}
}

func TestPluginSettingsAPI_POST_UpdatesDeclaredKeyIgnoresUndeclaredAndKeepsBlankSecret(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newPluginSettingsTestDeps(t)
	ctx := context.Background()
	seedPluginSetting(t, dp, "p1", "endpoint_url", "https://old.example", "global")
	seedPluginSetting(t, dp, "p1", "api_key", "original-secret", "global")

	form := "setting_endpoint_url=https%3A%2F%2Fnew.example" +
		"&setting_api_key=" + // blank secret submission -> keep current value
		"&setting_not_declared=should-be-ignored"
	req := httptest.NewRequest(http.MethodPost, "/api/plugins/p1/settings", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/plugins/p1/settings: code %d body %s", rec.Code, rec.Body.String())
	}

	rows, err := data.NewPluginRepo(dp.Db).ListPluginSettings(ctx, "p1")
	if err != nil {
		t.Fatalf("list plugin settings: %v", err)
	}
	got := map[string]string{}
	for _, row := range rows {
		var v string
		if json.Unmarshal([]byte(row.ValueJSON), &v) == nil {
			got[row.Key] = v
		}
	}
	if got["endpoint_url"] != "https://new.example" {
		t.Fatalf("expected endpoint_url updated, got %q", got["endpoint_url"])
	}
	if got["api_key"] != "original-secret" {
		t.Fatalf("expected a blank secret submission to keep the current value, got %q", got["api_key"])
	}
	if _, ok := got["not_declared"]; ok {
		t.Fatalf("expected an undeclared setting key to be silently ignored, not created")
	}

	var auditCount int
	if err := dp.Db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log WHERE action = 'plugin_settings_saved'`).Scan(&auditCount); err != nil {
		t.Fatalf("query audit log: %v", err)
	}
	if auditCount != 1 {
		t.Fatalf("expected one plugin_settings_saved audit row, got %d", auditCount)
	}
}

func TestPluginSettingsAPI_POST_PerTillScopeStaysPerTillOnUpdate(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newPluginSettingsTestDeps(t)
	ctx := context.Background()
	seedPluginSetting(t, dp, "p1", "reader_id", "reader-A", "register")

	form := "setting_reader_id=reader-B"
	req := httptest.NewRequest(http.MethodPost, "/api/plugins/p1/settings", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST: code %d body %s", rec.Code, rec.Body.String())
	}

	rows, err := data.NewPluginRepo(dp.Db).ListPluginSettings(ctx, "p1")
	if err != nil {
		t.Fatalf("list plugin settings: %v", err)
	}
	found := false
	for _, row := range rows {
		if row.Key != "reader_id" {
			continue
		}
		found = true
		if row.Scope != "register" {
			t.Fatalf("expected the updated setting to stay register-scoped, got %q", row.Scope)
		}
	}
	if !found {
		t.Fatalf("expected reader_id in the settings list")
	}
}
