package pages

import (
	"context"
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/secrets"
	"github.com/universaltill/universal-till/internal/settings"
)

// The page masks on plugins.IsSecretSettingKey — the very same rule
// internal/data seals on (ADR-0082); this pins the page-side expectations
// that predate the move out of this package.
func TestIsSecretSettingKey(t *testing.T) {
	secret := []string{
		"api_key", "apikey", "API_KEY", "my_secret_value", "token",
		"password", "passwd", "auth_value", "private_key", "webhook_key", "key",
	}
	for _, k := range secret {
		if !plugins.IsSecretSettingKey(k) {
			t.Errorf("expected %q to be classified as secret", k)
		}
	}
	plain := []string{"endpoint_url", "model_name", "currency", "timeout_seconds", ""}
	for _, k := range plain {
		if plugins.IsSecretSettingKey(k) {
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
		AuthSvc:  auth.NewService(db),
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
	if err := data.NewPluginRepo(db.Db).UpsertPluginSettingScoped(context.Background(), pluginID, key, string(raw), scope, false); err != nil {
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

// Positive counterpart to the two RequiresManager tests above (ut-docs#706 —
// canPerform()/Auth.Can(), not just the no-session short-circuit): a REAL
// session for cashier/manager/admin/super_admin must gate exactly as
// isManagerOrAuthOff did for manager/admin, with super_admin now also
// granted (#554/#555's noted broadening, accepted and inert since nothing
// today creates that role).
func TestPluginSettingsPages_RealSessionGatesByRole(t *testing.T) {
	t.Setenv("UT_AUTH", "on")
	for role, wantGET := range map[string]int{
		"cashier": http.StatusSeeOther, "manager": http.StatusOK,
		"admin": http.StatusOK, "super_admin": http.StatusOK,
	} {
		t.Run(role, func(t *testing.T) {
			mux, _ := newPluginSettingsTestDeps(t)

			getReq := auth.WithUser(httptest.NewRequest(http.MethodGet, "/plugins/p1/settings", nil), auth.User{ID: "u1", Role: role})
			getRec := httptest.NewRecorder()
			mux.ServeHTTP(getRec, getReq)
			if getRec.Code != wantGET {
				t.Fatalf("GET role=%s: got %d, want %d", role, getRec.Code, wantGET)
			}

			postReq := auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/plugins/p1/settings", strings.NewReader("setting_endpoint_url=x")), auth.User{ID: "u1", Role: role})
			postReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			postRec := httptest.NewRecorder()
			mux.ServeHTTP(postRec, postReq)
			wantPOST := http.StatusOK
			if role == "cashier" {
				wantPOST = http.StatusForbidden
			}
			if postRec.Code != wantPOST {
				t.Fatalf("POST role=%s: got %d, want %d", role, postRec.Code, wantPOST)
			}
		})
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

// seedTaxCode inserts a minimal active tax_codes row for the takeaway
// overrides editor tests.
func seedTaxCode(t *testing.T, db *common.Deps, id, name string, rateBP int, takeawayBP *int) {
	t.Helper()
	_, err := db.Db.ExecContext(context.Background(),
		`INSERT INTO tax_codes (id, name, rate_basis_points, is_active, takeaway_rate_basis_points) VALUES (?, ?, ?, 1, ?)`,
		id, name, rateBP, takeawayBP)
	if err != nil {
		t.Fatalf("seed tax code %s: %v", id, err)
	}
}

func TestPluginSettingsPage_GET_RendersTakeawayOverridesEditor(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newPluginSettingsTestDeps(t)
	seedTaxCode(t, dp, "tax_19", "Standard VAT", 1900, nil)
	// ut-docs#1676: "Reduced VAT" alone collides with 001_init.sql's own
	// tax_red seed row now that openPagesTestDB runs real migrations —
	// tax_codes.name is UNIQUE. Appending the rate keeps the
	// strings.Contains(body, "Reduced VAT") assertion below satisfied.
	seedTaxCode(t, dp, "tax_reduced", "Reduced VAT (7%)", 700, nil)
	seedPluginSetting(t, dp, "p1", "takeaway_rate_overrides", `{"tax_19":700}`, "global")

	req := httptest.NewRequest(http.MethodGet, "/plugins/p1/settings", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET: code %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "takeaway_pct_tax_19") {
		t.Fatalf("expected a takeaway_pct_tax_19 input, got %s", body)
	}
	if !strings.Contains(body, "takeaway_pct_tax_reduced") {
		t.Fatalf("expected a takeaway_pct_tax_reduced input, got %s", body)
	}
	if !strings.Contains(body, `value="7"`) {
		t.Fatalf("expected the pre-filled override (7%%) in the page, got %s", body)
	}
	// Exact names, not "Reduced VAT" alone -- 001_init.sql's own seeded
	// tax_red row is also named exactly "Reduced VAT" (ut-docs#1676), so a
	// bare substring match here would pass even if this test's own
	// tax_reduced row never rendered.
	if !strings.Contains(body, "Standard VAT") || !strings.Contains(body, "Reduced VAT (7%)") {
		t.Fatalf("expected tax code names in the page, got %s", body)
	}
}

func TestPluginSettingsPage_GET_RendersOrphanOverrideEntry(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newPluginSettingsTestDeps(t)
	// An override referencing "tax_gone", a tax code id that doesn't exist
	// (the real migration seeds its own active tax codes now, ut-docs#1676,
	// but none with this id) -- an orphaned override for a deleted one.
	seedPluginSetting(t, dp, "p1", "takeaway_rate_overrides", `{"tax_gone":500}`, "global")

	req := httptest.NewRequest(http.MethodGet, "/plugins/p1/settings", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET: code %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "takeaway_pct_tax_gone") {
		t.Fatalf("expected an orphan takeaway_pct_tax_gone input, got %s", body)
	}
	if !strings.Contains(body, `value="5"`) {
		t.Fatalf("expected the orphan's current override (5%%), got %s", body)
	}
}

func TestPluginSettingsPage_GET_FallsBackToRawInputWhenNoTaxCodesOrOverrides(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newPluginSettingsTestDeps(t)
	// 001_init.sql seeds three active tax codes (tax_std/tax_red/tax_zero)
	// now that openPagesTestDB runs real migrations (ut-docs#1676) --
	// deactivate all of them so this test genuinely reaches the "no active
	// tax codes" state, not just "one of three deactivated".
	if _, err := dp.Db.ExecContext(context.Background(), `UPDATE tax_codes SET is_active = 0`); err != nil {
		t.Fatal(err)
	}
	seedPluginSetting(t, dp, "p1", "takeaway_rate_overrides", `{}`, "global")

	req := httptest.NewRequest(http.MethodGet, "/plugins/p1/settings", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET: code %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `name="setting_takeaway_rate_overrides"`) {
		t.Fatalf("expected fallback to the plain text input, got %s", body)
	}
	if strings.Contains(body, "takeaway_pct_") {
		t.Fatalf("did not expect a typed row with no tax codes and no overrides, got %s", body)
	}
}

// settingsGetRoundTrip performs exactly the unwrap the settings_get WASM
// host fn does (internal/plugins/wasm_hostfns.go hostSettingsGet): a stored
// value_json is JSON-string-unwrapped, then the caller (the tax.de plugin's
// handleTaxRateAsk) json.Unmarshals the plain text into map[string]int.
func settingsGetRoundTrip(t *testing.T, valueJSON string) map[string]int {
	t.Helper()
	out := []byte(valueJSON)
	var str string
	if json.Unmarshal(out, &str) == nil {
		out = []byte(str)
	}
	var m map[string]int
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("round-trip unmarshal of %q: %v", valueJSON, err)
	}
	return m
}

func TestPluginSettingsAPI_POST_TypedTakeawayOverrides_StoresAndBumpsGeneration(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newPluginSettingsTestDeps(t)
	ctx := context.Background()
	seedTaxCode(t, dp, "tax_19", "Standard VAT", 1900, nil)
	seedPluginSetting(t, dp, "p1", "takeaway_rate_overrides", `{}`, "global")

	genBefore := plugins.SharedBus(dp.Db).Generation()

	form := "setting_takeaway_typed=1&takeaway_pct_tax_19=7"
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
	var stored string
	for _, row := range rows {
		if row.Key == "takeaway_rate_overrides" {
			stored = row.ValueJSON
		}
	}
	got := settingsGetRoundTrip(t, stored)
	want := map[string]int{"tax_19": 700}
	if got["tax_19"] != want["tax_19"] || len(got) != len(want) {
		t.Fatalf("round-tripped overrides = %+v, want %+v (raw stored: %s)", got, want, stored)
	}

	genAfter := plugins.SharedBus(dp.Db).Generation()
	if genAfter <= genBefore {
		t.Fatalf("expected BumpGeneration to run, generation before=%d after=%d", genBefore, genAfter)
	}
}

func TestPluginSettingsAPI_POST_TypedTakeawayOverrides_BlankClearsEntry(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newPluginSettingsTestDeps(t)
	ctx := context.Background()
	seedTaxCode(t, dp, "tax_19", "Standard VAT", 1900, nil)
	seedPluginSetting(t, dp, "p1", "takeaway_rate_overrides", `{"tax_19":700}`, "global")

	form := "setting_takeaway_typed=1&takeaway_pct_tax_19="
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
	var stored string
	for _, row := range rows {
		if row.Key == "takeaway_rate_overrides" {
			stored = row.ValueJSON
		}
	}
	got := settingsGetRoundTrip(t, stored)
	if _, ok := got["tax_19"]; ok {
		t.Fatalf("expected tax_19 cleared, got %+v (raw: %s)", got, stored)
	}
}

func TestPluginSettingsAPI_POST_TypedTakeawayOverrides_InvalidRateRejected(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newPluginSettingsTestDeps(t)
	ctx := context.Background()
	seedTaxCode(t, dp, "tax_19", "Standard VAT", 1900, nil)
	seedPluginSetting(t, dp, "p1", "takeaway_rate_overrides", `{}`, "global")

	form := "setting_takeaway_typed=1&takeaway_pct_tax_19=150"
	req := httptest.NewRequest(http.MethodPost, "/api/plugins/p1/settings", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an out-of-range rate, got %d body %s", rec.Code, rec.Body.String())
	}

	rows, err := data.NewPluginRepo(dp.Db).ListPluginSettings(ctx, "p1")
	if err != nil {
		t.Fatalf("list plugin settings: %v", err)
	}
	for _, row := range rows {
		if row.Key == "takeaway_rate_overrides" && row.ValueJSON != `"{}"` {
			t.Fatalf("expected nothing written on invalid input, got %s", row.ValueJSON)
		}
	}
}

func TestPluginSettingsAPI_POST_TypedTakeawayOverrides_OrphanEntryClearable(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newPluginSettingsTestDeps(t)
	ctx := context.Background()
	seedPluginSetting(t, dp, "p1", "takeaway_rate_overrides", `{"tax_gone":500}`, "global")

	form := "setting_takeaway_typed=1&takeaway_pct_tax_gone="
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
	var stored string
	for _, row := range rows {
		if row.Key == "takeaway_rate_overrides" {
			stored = row.ValueJSON
		}
	}
	got := settingsGetRoundTrip(t, stored)
	if _, ok := got["tax_gone"]; ok {
		t.Fatalf("expected orphan entry cleared, got %+v (raw: %s)", got, stored)
	}
}

func TestPluginSettingsAPI_POST_TypedTakeawayOverrides_IgnoresUnknownTaxCodeID(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newPluginSettingsTestDeps(t)
	ctx := context.Background()
	seedTaxCode(t, dp, "tax_19", "Standard VAT", 1900, nil)
	seedPluginSetting(t, dp, "p1", "takeaway_rate_overrides", `{}`, "global")

	// takeaway_pct_unknown_id doesn't match any active tax code or existing
	// orphan entry -- the form cannot invent an override entry. The known
	// field alongside it makes this test fail against code that never runs
	// the typed path at all (review finding: the unknown-only form was also
	// "ignored" by the pre-change code, asserting nothing).
	form := "setting_takeaway_typed=1&takeaway_pct_unknown_id=9&takeaway_pct_tax_19=7"
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
	var stored string
	for _, row := range rows {
		if row.Key == "takeaway_rate_overrides" {
			stored = row.ValueJSON
		}
	}
	got := settingsGetRoundTrip(t, stored)
	if _, ok := got["unknown_id"]; ok {
		t.Fatalf("expected the unknown tax_code_id ignored, got %+v (raw: %s)", got, stored)
	}
	if got["tax_19"] != 700 {
		t.Fatalf("expected the known tax code's override stored (tax_19=700), got %+v (raw: %s)", got, stored)
	}
}

// Regression for the ut-docs#190 review's BLOCKER: strconv.ParseFloat
// accepts NaN/Inf/overflowing exponents, and int(math.Round(x)) on those is
// implementation-defined — on amd64 it yields math.MinInt64, which the
// pre-fix code persisted into the live tax map with a success response
// (arm64 happened to round them to 0, which is why the original tests
// stayed green). All of these must be a 400 with nothing written.
func TestPluginSettingsAPI_POST_TypedTakeawayOverrides_NonFiniteAndOverflowRejected(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newPluginSettingsTestDeps(t)
	ctx := context.Background()
	seedTaxCode(t, dp, "tax_19", "Standard VAT", 1900, nil)
	seedPluginSetting(t, dp, "p1", "takeaway_rate_overrides", `{}`, "global")

	for _, bad := range []string{"NaN", "nan", "Inf", "+Inf", "-Inf", "infinity", "1e300", "101", "-1", "0", "0.004"} {
		form := "setting_takeaway_typed=1&takeaway_pct_tax_19=" + bad
		req := httptest.NewRequest(http.MethodPost, "/api/plugins/p1/settings", strings.NewReader(form))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("input %q: expected 400, got %d body %s", bad, rec.Code, rec.Body.String())
		}
	}

	rows, err := data.NewPluginRepo(dp.Db).ListPluginSettings(ctx, "p1")
	if err != nil {
		t.Fatalf("list plugin settings: %v", err)
	}
	for _, row := range rows {
		if row.Key == "takeaway_rate_overrides" {
			if got := settingsGetRoundTrip(t, row.ValueJSON); len(got) != 0 {
				t.Fatalf("expected nothing written after rejected inputs, got %+v (raw: %s)", got, row.ValueJSON)
			}
		}
	}
}

// Regression for the ut-docs#190 review's lost-update finding: a form
// rendered before an entry existed (no takeaway_pct_* field for it at all)
// must PRESERVE that entry on save, not silently delete it — a concurrent
// writer (e.g. a catalog import merging into this same global row) may have
// added it after the page loaded. Present-but-blank remains an explicit
// removal (covered by ..._BlankClearsEntry).
func TestPluginSettingsAPI_POST_TypedTakeawayOverrides_AbsentFieldPreservesEntry(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newPluginSettingsTestDeps(t)
	ctx := context.Background()
	seedTaxCode(t, dp, "tax_19", "Standard VAT", 1900, nil)
	// ut-docs#1676: same tax_codes.name UNIQUE collision as above.
	seedTaxCode(t, dp, "tax_7", "Reduced VAT (7%)", 700, nil)
	seedPluginSetting(t, dp, "p1", "takeaway_rate_overrides", `{"tax_19":700,"tax_7":500}`, "global")

	// The stale form carries a field for tax_19 only — tax_7's entry must
	// survive the save untouched.
	form := "setting_takeaway_typed=1&takeaway_pct_tax_19=8"
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
	for _, row := range rows {
		if row.Key == "takeaway_rate_overrides" {
			got := settingsGetRoundTrip(t, row.ValueJSON)
			if got["tax_19"] != 800 || got["tax_7"] != 500 {
				t.Fatalf("expected {tax_19:800, tax_7:500}, got %+v (raw: %s)", got, row.ValueJSON)
			}
		}
	}
}

// Regression for the ut-docs#190 review's partial-write finding: settings
// rows are written in key order, so a validation failure on the typed
// takeaway row used to land AFTER sibling keys sorting before it had
// already been upserted — half the form committed, no generation bump, no
// audit row. Validation now runs before any write: the whole POST must
// abort with the sibling setting untouched.
func TestPluginSettingsAPI_POST_TypedTakeawayOverrides_InvalidRateLeavesSiblingSettingsUnwritten(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newPluginSettingsTestDeps(t)
	ctx := context.Background()
	seedTaxCode(t, dp, "tax_19", "Standard VAT", 1900, nil)
	// "api_endpoint" sorts before "takeaway_rate_overrides", so under the
	// old mid-loop abort it was already written when validation failed.
	seedPluginSetting(t, dp, "p1", "api_endpoint", "http://old.local", "global")
	seedPluginSetting(t, dp, "p1", "takeaway_rate_overrides", `{}`, "global")

	form := "setting_api_endpoint=http%3A%2F%2Fnew.local&setting_takeaway_typed=1&takeaway_pct_tax_19=150"
	req := httptest.NewRequest(http.MethodPost, "/api/plugins/p1/settings", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST: expected 400, got %d body %s", rec.Code, rec.Body.String())
	}

	rows, err := data.NewPluginRepo(dp.Db).ListPluginSettings(ctx, "p1")
	if err != nil {
		t.Fatalf("list plugin settings: %v", err)
	}
	for _, row := range rows {
		if row.Key == "api_endpoint" {
			var v string
			if json.Unmarshal([]byte(row.ValueJSON), &v) == nil && v != "http://old.local" {
				t.Fatalf("sibling setting was written despite the aborted POST: %q", v)
			}
		}
	}
}

// Regression for the ut-docs#190 review's silent-clobber finding: a stored
// value that doesn't parse as a tax_code_id -> bp map (hand-edited raw
// JSON) must render as the RAW text input — visible and fixable — not as an
// all-blank typed editor whose next save would overwrite it.
func TestPluginSettingsPage_GET_UnparseableStoredValueFallsBackToRawInput(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newPluginSettingsTestDeps(t)
	seedTaxCode(t, dp, "tax_19", "Standard VAT", 1900, nil)
	seedPluginSetting(t, dp, "p1", "takeaway_rate_overrides", `{oops`, "global")

	req := httptest.NewRequest(http.MethodGet, "/plugins/p1/settings", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET: code %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "setting_takeaway_typed") {
		t.Fatalf("expected raw-input fallback for an unparseable stored value, got the typed editor")
	}
	if !strings.Contains(body, `name="setting_takeaway_rate_overrides"`) {
		t.Fatalf("expected the raw text input rendering the stored value, body:\n%s", body)
	}
}

// --- ut-docs#946 (924 increment 4): raw err.Error() leaks now route through
// httpx.RenderError (ut-docs#1663 migrated these page-route sites off
// common.LogAndLocalizedError). Each test below forces a REAL failure (a
// dropped table or a read-only connection, never a mock/stub repo) at one
// specific call site and asserts the localized "plugins.error.server"
// copy appears while the raw SQL/Go error text does not.
//
// Line 308's fallback (a non-httpStatusError from parseTaxOverrides) is not
// covered by a forced-failure test: parseTaxOverrides' only non-nil error
// return is the httpStatusError value at its "invalid" branch, so that
// fallback is unreachable through this handler as coded today — same
// justified-skip reasoning increment 2's review accepted for
// buttons_api.go's four unreachable ui.NewRenderer sites. The call site is
// still fixed (routed through common.LogAndLocalizedError with the same
// plugins.error.server key) so it stays defensively correct if
// parseTaxOverrides is ever changed to return a different error type.

func TestPluginSettingsPage_GET_ListFailureIsLocalized(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newPluginSettingsTestDeps(t)
	if _, err := dp.Db.Exec(`DROP TABLE plugin_settings`); err != nil {
		t.Fatalf("drop plugin_settings: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/plugins/p1/settings", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	want := httpx.T("en", "plugins.error.server")
	if !strings.Contains(body, want) {
		t.Fatalf("expected the localized message %q, got %q", want, body)
	}
	if strings.Contains(body, "no such table") {
		t.Fatalf("raw SQL error leaked into the response: %q", body)
	}
}

// catalogRepo.ListTaxCodes fails AFTER repo.ListPluginSettings has already
// succeeded and found a takeaway_rate_overrides row — dropping only
// tax_codes (leaving plugin_settings intact) isolates this call site
// (line 251) from the ListPluginSettings one (line 234) above.
func TestPluginSettingsPage_GET_TaxCodesFailureIsLocalized(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newPluginSettingsTestDeps(t)
	seedPluginSetting(t, dp, "p1", "takeaway_rate_overrides", `{}`, "global")
	// ut-docs#1679: DROP TABLE tax_codes used to force this, but tax_codes
	// now has real incoming FKs that block the DROP under real migrations.
	// A closed *sql.DB forces the same generic repo-error path instead.
	dp.Db.Close()

	req := httptest.NewRequest(http.MethodGet, "/plugins/p1/settings", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	want := httpx.T("en", "plugins.error.server")
	if !strings.Contains(body, want) {
		t.Fatalf("expected the localized message %q, got %q", want, body)
	}
	if strings.Contains(body, "no such table") {
		t.Fatalf("raw SQL error leaked into the response: %q", body)
	}
}

func TestPluginSettingsAPI_POST_ListFailureIsLocalized(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newPluginSettingsTestDeps(t)
	if _, err := dp.Db.Exec(`DROP TABLE plugin_settings`); err != nil {
		t.Fatalf("drop plugin_settings: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/plugins/p1/settings", strings.NewReader("setting_x=y"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	want := httpx.T("en", "plugins.error.server")
	if !strings.Contains(body, want) {
		t.Fatalf("expected the localized message %q, got %q", want, body)
	}
	if strings.Contains(body, "no such table") {
		t.Fatalf("raw SQL error leaked into the response: %q", body)
	}
}

// Same isolation approach as the GET test above, but on the POST path's
// own ListTaxCodes call (line 297), reached only when
// setting_takeaway_typed=1 is submitted.
func TestPluginSettingsAPI_POST_TaxCodesFailureIsLocalized(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newPluginSettingsTestDeps(t)
	seedPluginSetting(t, dp, "p1", "takeaway_rate_overrides", `{}`, "global")
	// ut-docs#1679: see TestPluginSettingsPage_GET_TaxCodesFailureIsLocalized.
	dp.Db.Close()

	form := "setting_takeaway_typed=1&takeaway_pct_tax_19=7"
	req := httptest.NewRequest(http.MethodPost, "/api/plugins/p1/settings", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	want := httpx.T("en", "plugins.error.server")
	if !strings.Contains(body, want) {
		t.Fatalf("expected the localized message %q, got %q", want, body)
	}
	if strings.Contains(body, "no such table") {
		t.Fatalf("raw SQL error leaked into the response: %q", body)
	}
}

// writeTaxOverrides' repo.UpsertPluginSettingScoped fails on a read-only
// connection while the earlier ListPluginSettings/ListTaxCodes SELECTs and
// parseTaxOverrides validation all still succeed — isolates line 318.
func TestPluginSettingsAPI_POST_TypedWriteFailureIsLocalized(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newPluginSettingsTestDeps(t)
	ctx := context.Background()
	seedTaxCode(t, dp, "tax_19", "Standard VAT", 1900, nil)
	seedPluginSetting(t, dp, "p1", "takeaway_rate_overrides", `{}`, "global")

	if _, err := dp.Db.ExecContext(ctx, `PRAGMA query_only = ON`); err != nil {
		t.Fatalf("set query_only: %v", err)
	}
	t.Cleanup(func() { _, _ = dp.Db.Exec(`PRAGMA query_only = OFF`) })

	form := "setting_takeaway_typed=1&takeaway_pct_tax_19=7"
	req := httptest.NewRequest(http.MethodPost, "/api/plugins/p1/settings", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	want := httpx.T("en", "plugins.error.server")
	if !strings.Contains(body, want) {
		t.Fatalf("expected the localized message %q, got %q", want, body)
	}
	if strings.Contains(body, "readonly database") {
		t.Fatalf("raw SQL error leaked into the response: %q", body)
	}
}

// The plain (non-typed) settings write at line 346 fails on a read-only
// connection while ListPluginSettings still succeeds — a different branch
// of the same POST handler's write loop from the typed-overrides case
// above, exercised with no setting_takeaway_typed field at all.
func TestPluginSettingsAPI_POST_PlainWriteFailureIsLocalized(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newPluginSettingsTestDeps(t)
	seedPluginSetting(t, dp, "p1", "endpoint_url", "https://old.example", "global")

	if _, err := dp.Db.Exec(`PRAGMA query_only = ON`); err != nil {
		t.Fatalf("set query_only: %v", err)
	}
	t.Cleanup(func() { _, _ = dp.Db.Exec(`PRAGMA query_only = OFF`) })

	form := "setting_endpoint_url=https%3A%2F%2Fnew.example"
	req := httptest.NewRequest(http.MethodPost, "/api/plugins/p1/settings", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	want := httpx.T("en", "plugins.error.server")
	if !strings.Contains(body, want) {
		t.Fatalf("expected the localized message %q, got %q", want, body)
	}
	if strings.Contains(body, "readonly database") {
		t.Fatalf("raw SQL error leaked into the response: %q", body)
	}
}

// ADR-0082 (ut-docs#1739): a key the manifest declares `type: "secret"` but
// whose NAME matches no credential heuristic ("merchant_code") must be
// treated exactly like a heuristic secret by the page — masked on GET,
// sealed at rest on POST. The manifest is read from the plugin's on-disk
// install tree (paths.Plugins(id, version, "manifest.json")), which is the
// only place the declaration exists (plugin_settings has no type column).
func TestPluginSettingsPage_ManifestDeclaredSecretIsMaskedAndSealed(t *testing.T) {
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
		"settings":[{"key":"merchant_code","type":"secret"},{"key":"endpoint_url"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if plugins.IsSecretSettingKey("merchant_code") {
		t.Fatal("test premise: merchant_code must NOT match the key-name heuristic")
	}
	// Seeded the way a pre-ADR-0082 row would be: plain JSON at rest.
	seedPluginSetting(t, dp, "p1", "merchant_code", "M-old", "global")
	seedPluginSetting(t, dp, "p1", "endpoint_url", "https://plugin.example/api", "global")

	req := httptest.NewRequest(http.MethodGet, "/plugins/p1/settings", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET: code %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "M-old") {
		t.Fatalf("a manifest-declared secret's value must never be sent to the page, got %q", body)
	}
	if !strings.Contains(body, `type="password" name="setting_merchant_code"`) {
		t.Fatalf("merchant_code must render as a masked password field, got %q", body)
	}
	if !strings.Contains(body, "https://plugin.example/api") {
		t.Fatalf("the undeclared sibling must still render plain, got %q", body)
	}

	form := strings.NewReader("setting_merchant_code=M-new&setting_endpoint_url=https://plugin.example/api")
	req = httptest.NewRequest(http.MethodPost, "/api/plugins/p1/settings", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST: code %d body %s", rec.Code, rec.Body.String())
	}
	var raw string
	if err := dp.Db.QueryRowContext(ctx, `SELECT value_json FROM plugin_settings WHERE plugin_id = 'p1' AND key = 'merchant_code'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if !secrets.IsSealed(raw) || strings.Contains(raw, "M-new") {
		t.Fatalf("manifest-declared secret at rest = %q, want sealed", raw)
	}
	if got, found, err := data.NewPluginRepo(dp.Db).GetPluginSetting(ctx, "p1", "merchant_code"); err != nil || !found || got != `"M-new"` {
		t.Fatalf("GetPluginSetting = %q %v %v", got, found, err)
	}
	if err := dp.Db.QueryRowContext(ctx, `SELECT value_json FROM plugin_settings WHERE plugin_id = 'p1' AND key = 'endpoint_url'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != `"https://plugin.example/api"` {
		t.Fatalf("undeclared sibling at rest = %q, want plain JSON", raw)
	}
}

// TestPluginSettingsAPI_POST_RefusesWriteWhenManifestUnreadable is the
// ut-docs#1739 review's blocker fix, reproduced and pinned as a regression
// test: a plugin with an active version whose manifest.json EXISTS but
// fails to parse (corrupt file, not merely absent — see
// plugins.InstalledManifest's doc comment for why "absent" stays a
// tolerated no-op) must refuse the write rather than silently store a
// possibly-manifest-declared-secret value in plaintext. Before the fix,
// this POST answered 200 and wrote merchant_code — a key the heuristic
// does not catch — as plain JSON.
func TestPluginSettingsAPI_POST_RefusesWriteWhenManifestUnreadable(t *testing.T) {
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
	// Deliberately malformed JSON — manifest.json EXISTS (unlike the
	// tolerated "absent" case) but cannot be parsed.
	if err := os.WriteFile(filepath.Join(manifestDir, "manifest.json"), []byte(`{not valid json`), 0o644); err != nil {
		t.Fatal(err)
	}
	if plugins.IsSecretSettingKey("merchant_code") {
		t.Fatal("test premise: merchant_code must NOT match the key-name heuristic")
	}
	seedPluginSetting(t, dp, "p1", "merchant_code", "M-old", "global")

	form := strings.NewReader("setting_merchant_code=M-new")
	req := httptest.NewRequest(http.MethodPost, "/api/plugins/p1/settings", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("POST with an unparseable manifest: code %d body %s, want 500 (refused)", rec.Code, rec.Body.String())
	}

	var raw string
	if err := dp.Db.QueryRowContext(ctx, `SELECT value_json FROM plugin_settings WHERE plugin_id = 'p1' AND key = 'merchant_code'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != `"M-old"` {
		t.Fatalf("refused write must leave the existing row untouched, got %q", raw)
	}
	if strings.Contains(raw, "M-new") {
		t.Fatalf("the new value must never reach the database when the manifest can't be resolved, got %q", raw)
	}
}

// ADR-0085 (ut-docs#1708): the AI plugin's api_key field carries a
// data-protection notice ABOVE its input — the operator reads what a hosted
// provider receives before they can type a key. It is specific to that one
// plugin's one key: every other plugin's api_key (p1's here) and the AI
// plugin's own non-secret settings render exactly as before.
func TestPluginSettingsPage_GET_AIPluginAPIKeyShowsHostedProviderNotice(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newPluginSettingsTestDeps(t)
	seedAIPluginRows(t, dp.Db, true)
	seedPluginSetting(t, dp, AIPluginID, "provider", "self_hosted", "global")
	seedPluginSetting(t, dp, AIPluginID, "endpoint", "http://localhost:11434", "global")
	// A real value, not empty (review finding, ut-docs#1708): the empty seed
	// this test used before couldn't distinguish "masked" from "nothing to
	// leak in the first place". TestPluginSettingsPage_GET_RendersPlainValueAndMasksSecret
	// already proves the underlying isSecret/masking path generically; this
	// asserts the same property specifically for the AI plugin's own key.
	seedPluginSetting(t, dp, AIPluginID, "api_key", "sk-ant-should-never-render", "global")
	seedPluginSetting(t, dp, "p1", "api_key", "other-plugins-secret", "global")

	notice := httpx.T("en", "plugins.settings.ai.hosted_provider_notice")
	if notice == "" || notice == "plugins.settings.ai.hosted_provider_notice" {
		t.Fatalf("notice key must resolve to real copy in en.json, got %q", notice)
	}
	// html/template escapes the copy's apostrophes (&#39;) on the way out —
	// compare what the browser actually receives.
	notice = html.EscapeString(notice)

	req := httptest.NewRequest(http.MethodGet, "/plugins/"+AIPluginID+"/settings", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET AI plugin settings: code %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if n := strings.Count(body, notice); n != 1 {
		t.Fatalf("expected the hosted-provider notice exactly once (api_key only, not endpoint/provider), got %d in:\n%s", n, body)
	}
	noticeAt := strings.Index(body, notice)
	inputAt := strings.Index(body, `name="setting_api_key"`)
	if inputAt < 0 {
		t.Fatalf("expected the masked api_key input, got:\n%s", body)
	}
	if noticeAt > inputAt {
		t.Fatalf("the notice must render ABOVE the api_key input (notice at %d, input at %d)", noticeAt, inputAt)
	}
	if !strings.Contains(body, `type="password" name="setting_api_key"`) {
		t.Fatalf("api_key must still render masked, got:\n%s", body)
	}
	if strings.Contains(body, "sk-ant-should-never-render") {
		t.Fatal("the AI plugin's own api_key value must never render into the page")
	}

	// Another plugin's api_key is a plain secret field: no notice.
	req = httptest.NewRequest(http.MethodGet, "/plugins/p1/settings", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET p1 settings: code %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), notice) {
		t.Fatal("the hosted-provider notice must only render for the AI plugin's api_key, not every plugin's secret field")
	}
}
