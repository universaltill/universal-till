package pages

import (
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/fiscal"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Additional-till side of ut-docs#2791: on a till that follows a main till,
// a shop-wide setting changed in Settings is written through to the main
// till's /api/sync/settings/apply and mirrored locally only after the main
// till answered 200. Any failure refuses the change with no local write --
// a local-only edit would be silently overwritten by the next admin-bundle
// pull. Per-till settings still save locally. The main till here is the
// REAL handler (registerSyncSettings) behind an httptest server with its own
// database.

const (
	settingsUnreachableEN = "Can't reach the main till — change this setting on the main till, or try again when it's back."
	settingsRefusedEN     = "The main till refused this change."
)

type settingsSyncMain struct {
	*syncSettingsMain
	srv   *httptest.Server
	calls atomic.Int64
}

func newSettingsSyncMain(t *testing.T) *settingsSyncMain {
	t.Helper()
	m := &settingsSyncMain{syncSettingsMain: newSyncSettingsTestDeps(t)}
	// The replica's operators, as the admin bundle delivered them.
	insertTestUserWithPIN(t, m.dp.Db, "m1", "m1", "Mgr", "manager", "")
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.calls.Add(1)
		m.mux.ServeHTTP(w, r)
	}))
	t.Cleanup(m.srv.Close)
	return m
}

// newSettingsSyncReplica is the additional till: the full settings handlers
// over its own database, pointed at mainURL.
func newSettingsSyncReplica(t *testing.T, mainURL string) (*http.ServeMux, *common.Deps) {
	t.Helper()
	mux, _, dp := newFullAuthDeps(t)
	setReplicaSettings(t, dp.Settings, mainURL, syncSettingsBearer)
	return mux, dp
}

func wantSettingsFragmentRefused(t *testing.T, rec *httptest.ResponseRecorder, msg string) {
	t.Helper()
	// The fragment is HTML: the message arrives escaped.
	if !strings.Contains(rec.Body.String(), `class="error"`) || !strings.Contains(rec.Body.String(), html.EscapeString(msg)) {
		t.Fatalf("status=%d body=%q, want an error fragment with %q", rec.Code, rec.Body.String(), msg)
	}
}

func TestSettingsWriteThrough_OrderTypePromptLandsOnMainAndMirrors(t *testing.T) {
	main := newSettingsSyncMain(t)
	mux, dp := newSettingsSyncReplica(t, main.srv.URL)
	httpx.InitOrderTypePromptMode("top")
	t.Cleanup(func() { httpx.InitOrderTypePromptMode("top") })

	rec := postForm(mux, "/api/settings/order-type-prompt", url.Values{"mode": {data.OrderTypePromptModeAtPay}}, &mgrUser)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "✓") {
		t.Fatalf("replica order-type-prompt = %d %q", rec.Code, rec.Body.String())
	}
	if main.calls.Load() != 1 {
		t.Fatalf("main till calls = %d, want 1", main.calls.Load())
	}
	if got := mustSetting(t, main.dp, data.OrderTypePromptModeKey); got != data.OrderTypePromptModeAtPay {
		t.Fatalf("main till %s = %q, want at_pay — the change must land on the main till", data.OrderTypePromptModeKey, got)
	}
	if got := mustSetting(t, dp, data.OrderTypePromptModeKey); got != data.OrderTypePromptModeAtPay {
		t.Fatalf("replica %s = %q, want the mirrored at_pay", data.OrderTypePromptModeKey, got)
	}
	if fn := httpx.FuncsFor("en")["ordertypepromptmode"].(func() string); fn() != data.OrderTypePromptModeAtPay {
		t.Fatalf("replica live prompt mode = %q, want at_pay", fn())
	}
	assertSettingSyncAudit(t, main.dp, "m1", data.OrderTypePromptModeKey, data.OrderTypePromptModeAtPay, "Till 2")
	if main.refreshN.Load() != 1 {
		t.Fatalf("main till refresh calls = %d, want 1", main.refreshN.Load())
	}
}

// Main till unreachable: refused with the localized message, the replica's
// value and live state untouched, no local audit.
func TestSettingsWriteThrough_MainUnreachableRefusesWithoutLocalWrite(t *testing.T) {
	mux, dp := newSettingsSyncReplica(t, deadPrimaryURL())
	httpx.InitOrderTypePromptMode("top")
	t.Cleanup(func() { httpx.InitOrderTypePromptMode("top") })
	if err := dp.Settings.Set(t.Context(), data.OrderTypePromptModeKey, "top"); err != nil {
		t.Fatal(err)
	}

	wantSettingsFragmentRefused(t, postForm(mux, "/api/settings/order-type-prompt", url.Values{"mode": {"at_pay"}}, &mgrUser), settingsUnreachableEN)
	wantSettingsFragmentRefused(t, postForm(mux, "/api/settings/payments-default", url.Values{"method": {"card"}}, &mgrUser), settingsUnreachableEN)
	wantSettingsFragmentRefused(t, postForm(mux, "/api/settings/order-no-scheme", url.Values{"scheme": {data.DisplayNoSchemeLifetimeNoReset}}, &mgrUser), settingsUnreachableEN)
	wantSettingsFragmentRefused(t, postForm(mux, "/api/settings/payments-fee", url.Values{"method": {"card"}, "percent": {"1.5"}, "fixed": {"0.2"}}, &mgrUser), settingsUnreachableEN)
	rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {"store.name"}, "value": {"New Name"}}, &mgrUser)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), settingsUnreachableEN) {
		t.Fatalf("upsert refused = %d %q, want 502 with the unreachable message", rec.Code, rec.Body.String())
	}

	if got := mustSetting(t, dp, data.OrderTypePromptModeKey); got != "top" {
		t.Fatalf("refused change wrote locally: %s = %q", data.OrderTypePromptModeKey, got)
	}
	for _, k := range []string{"payments.default_method", data.SaleDisplayNoSchemeKey, "payments.fee.card"} {
		if got := mustSetting(t, dp, k); got != "" {
			t.Fatalf("refused change wrote locally: %s = %q", k, got)
		}
	}
	if got := mustSetting(t, dp, "store.name"); got == "New Name" {
		t.Fatal("refused upsert wrote locally")
	}
	if fn := httpx.FuncsFor("en")["ordertypepromptmode"].(func() string); fn() != "top" {
		t.Fatalf("refused change moved the live prompt mode to %q", fn())
	}
	entries, err := data.NewPOSRepo(dp.Db).ListAudit(t.Context(), data.AuditFilters{EntityType: "settings"})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("refused changes were audited locally: %+v", entries)
	}
}

// A replica with the main till's URL but no bearer yet is refused too,
// rather than silently writing locally.
func TestSettingsWriteThrough_NoBearerRefuses(t *testing.T) {
	mux, _, dp := newFullAuthDeps(t)
	setReplicaSettings(t, dp.Settings, "http://192.0.2.1:1", "")
	wantSettingsFragmentRefused(t, postForm(mux, "/api/settings/payments-default", url.Values{"method": {"card"}}, &mgrUser), settingsUnreachableEN)
	if got := mustSetting(t, dp, "payments.default_method"); got != "" {
		t.Fatalf("refused change wrote locally: %q", got)
	}
}

// The main till decides with ITS roles: an operator this till's stale
// mirror still thinks is a manager, but whom the main till knows as a
// cashier, is refused with a 403 -- the local permission refusal's shape.
func TestSettingsWriteThrough_MainForbiddenIs403(t *testing.T) {
	main := newSettingsSyncMain(t)
	mux, dp := newSettingsSyncReplica(t, main.srv.URL)
	insertTestUserWithPIN(t, main.dp.Db, "x-1", "xan", "Xan", "cashier", "")
	insertTestUserWithPIN(t, dp.Db, "x-1", "xan", "Xan", "manager", "")
	xan := auth.User{ID: "x-1", Role: "manager"}

	for _, tc := range []struct {
		path string
		form url.Values
	}{
		{"/api/settings/order-type-prompt", url.Values{"mode": {"at_pay"}}},
		{"/api/settings/payments-default", url.Values{"method": {"card"}}},
		{"/api/settings/upsert", url.Values{"key": {"store.name"}, "value": {"X"}}},
	} {
		if rec := postForm(mux, tc.path, tc.form, &xan); rec.Code != http.StatusForbidden {
			t.Fatalf("%s = %d %q, want 403", tc.path, rec.Code, rec.Body.String())
		}
	}
	if got := mustSetting(t, dp, data.OrderTypePromptModeKey); got != "" {
		t.Fatalf("forbidden change wrote locally: %q", got)
	}
}

// A main-till refusal that is not a permission refusal (here: the "main
// till" itself follows another till) shows the refused message.
func TestSettingsWriteThrough_MainRefusalShowsRefusedMessage(t *testing.T) {
	main := newSettingsSyncMain(t)
	setReplicaSettings(t, main.dp.Settings, "http://192.0.2.1:8080", "b-123")
	mux, dp := newSettingsSyncReplica(t, main.srv.URL)

	wantSettingsFragmentRefused(t, postForm(mux, "/api/settings/payments-default", url.Values{"method": {"card"}}, &mgrUser), settingsRefusedEN)
	rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {"store.name"}, "value": {"X"}}, &mgrUser)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), settingsRefusedEN) {
		t.Fatalf("upsert = %d %q, want 409 with the refused message", rec.Code, rec.Body.String())
	}
	if got := mustSetting(t, dp, "payments.default_method"); got != "" {
		t.Fatalf("refused change wrote locally: %q", got)
	}
}

// The generic editor: a shop-wide key goes through the main till; a
// per-till key is saved locally without calling it; store.country is
// refused on an additional till before anything local is touched.
func TestSettingsWriteThrough_Upsert(t *testing.T) {
	main := newSettingsSyncMain(t)
	mux, dp := newSettingsSyncReplica(t, main.srv.URL)

	rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {"store.name"}, "value": {"Corner Shop"}}, &mgrUser)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("upsert shop-wide = %d %q", rec.Code, rec.Body.String())
	}
	if mustSetting(t, main.dp, "store.name") != "Corner Shop" || mustSetting(t, dp, "store.name") != "Corner Shop" {
		t.Fatal("shop-wide upsert must land on the main till and be mirrored")
	}

	before := main.calls.Load()
	rec = postForm(mux, "/api/settings/upsert", url.Values{"key": {"printer.host"}, "value": {"10.0.0.9"}}, &mgrUser)
	if rec.Code != http.StatusNoContent || mustSetting(t, dp, "printer.host") != "10.0.0.9" {
		t.Fatalf("per-till upsert = %d, local value %q", rec.Code, mustSetting(t, dp, "printer.host"))
	}
	if main.calls.Load() != before || mustSetting(t, main.dp, "printer.host") != "" {
		t.Fatal("a per-till key must never reach the main till")
	}

}

// A PIN-elevated change carries the approver: the main till decides with
// the approver's role and audits both.
func TestSettingsWriteThrough_ElevatedCarriesApprover(t *testing.T) {
	main := newSettingsSyncMain(t)
	mux, dp := newSettingsSyncReplica(t, main.srv.URL)
	pin, err := auth.HashPIN("5151")
	if err != nil {
		t.Fatal(err)
	}
	insertTestUserWithPIN(t, main.dp.Db, "m2", "m2", "Mo", "manager", pin)
	insertTestUserWithPIN(t, dp.Db, "m2", "m2", "Mo", "manager", pin)
	insertTestUserWithPIN(t, main.dp.Db, "c1", "c1", "Cash", "cashier", "")
	insertTestUserWithPIN(t, dp.Db, "c1", "c1", "Cash", "cashier", "")

	rec := postForm(mux, "/api/settings/payments-default", url.Values{"method": {"card"}, "override_pin": {"5151"}}, &cashUser)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "✓") {
		t.Fatalf("elevated payments-default = %d %q", rec.Code, rec.Body.String())
	}
	if mustSetting(t, main.dp, "payments.default_method") != "card" {
		t.Fatal("elevated change did not land on the main till")
	}
	assertSettingSyncAudit(t, main.dp, "m2", "payments.default_method", "card", "Till 2")
}

// Setting values never appear in anything logged on either till.
func TestSettingsWriteThrough_NeverLogsValues(t *testing.T) {
	var buf lockedBuffer
	restore := logging.CaptureForTest(&buf)
	t.Cleanup(restore)

	main := newSettingsSyncMain(t)
	mux, dp := newSettingsSyncReplica(t, main.srv.URL)
	const secret = "Zq-7731-seller-secret"
	if rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {"invoice.seller_vat_no"}, "value": {secret}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("upsert = %d %q", rec.Code, rec.Body.String())
	}
	setReplicaSettings(t, dp.Settings, deadPrimaryURL(), syncSettingsBearer)
	_ = postForm(mux, "/api/settings/upsert", url.Values{"key": {"invoice.seller_vat_no"}, "value": {secret}}, &mgrUser)

	out := buf.String()
	if !strings.Contains(out, "sync settings: invoice.seller_vat_no applied") || !strings.Contains(out, "settings sync: main till unreachable") {
		t.Fatalf("log capture did not see the write-through lines:\n%s", out)
	}
	if strings.Contains(out, secret) {
		t.Fatalf("log output leaks a setting value:\n%s", out)
	}
}

// Review of ut-docs#2791: keys a write implies travel in the SAME batch.
// A currency change and its confirmation are one main-till transaction (a
// second round-trip could land the currency without it), and a locale change
// bumps store.locale_generation on the main till, not only locally where the
// next pull would revert it.
func TestSettingsWriteThrough_UpsertImpliedKeysOneBatch(t *testing.T) {
	main := newSettingsSyncMain(t)
	mux, dp := newSettingsSyncReplica(t, main.srv.URL)

	rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {common.KeyCurrency}, "value": {"EUR"}}, &mgrUser)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("upsert currency = %d %q", rec.Code, rec.Body.String())
	}
	if main.calls.Load() != 1 {
		t.Fatalf("main till calls = %d, want 1 (currency and its confirmation in one batch)", main.calls.Load())
	}
	if mustSetting(t, main.dp, common.KeyCurrency) != "EUR" || mustSetting(t, main.dp, common.KeyCurrencyConfirmed) != "true" {
		t.Fatalf("main till currency=%q confirmed=%q, want EUR/true",
			mustSetting(t, main.dp, common.KeyCurrency), mustSetting(t, main.dp, common.KeyCurrencyConfirmed))
	}

	if err := main.dp.Settings.Set(t.Context(), common.KeyLocaleGeneration, "3"); err != nil {
		t.Fatal(err)
	}
	if err := dp.Settings.Set(t.Context(), common.KeyLocaleGeneration, "3"); err != nil {
		t.Fatal(err)
	}
	rec = postForm(mux, "/api/settings/upsert", url.Values{"key": {common.KeyLocale}, "value": {"tr"}}, &mgrUser)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("upsert locale = %d %q", rec.Code, rec.Body.String())
	}
	if main.calls.Load() != 2 {
		t.Fatalf("main till calls = %d, want 2", main.calls.Load())
	}
	if got := mustSetting(t, main.dp, common.KeyLocaleGeneration); got != "4" {
		t.Fatalf("main till %s = %q, want 4 — the bump must reach the main till", common.KeyLocaleGeneration, got)
	}
	if got := mustSetting(t, dp, common.KeyLocaleGeneration); got != "4" {
		t.Fatalf("replica %s = %q, want the mirrored 4", common.KeyLocaleGeneration, got)
	}
}

// ut-docs#2980: store.country from an additional till goes through the
// main till, which runs the owner check and the posture reset against its
// own state; the additional till's fiscal state is left for its next pull.
func newCountrySyncPair(t *testing.T) (*settingsSyncMain, *http.ServeMux, *common.Deps) {
	t.Helper()
	main := newSettingsSyncMain(t)
	seedMainCountry(t, main.dp, "GB", true)
	mux, dp := newSettingsSyncReplica(t, main.srv.URL)
	insertTestUserWithPIN(t, dp.Db, "adm-1", "adm1", "Admin One", "admin", "")
	for k, v := range map[string]string{common.KeyCountry: "GB", fiscal.SigningDeviceConfiguredKey("GB"): "true"} {
		if err := dp.Settings.Set(t.Context(), k, v); err != nil {
			t.Fatal(err)
		}
	}
	dp.UpdateState(func(s *common.RuntimeState) { s.Country = "GB" })
	return main, mux, dp
}

var ownerOnReplica = auth.User{ID: "adm-1", Role: "admin", DisplayName: "Admin One"}

func wantCountryThroughMain(t *testing.T, main *settingsSyncMain, dp *common.Deps) {
	t.Helper()
	if got := mustSetting(t, main.dp, common.KeyCountry); got != "DE" {
		t.Fatalf("main till %s = %q, want DE", common.KeyCountry, got)
	}
	if got := mustSetting(t, main.dp, fiscal.SigningDeviceConfiguredKey("GB")); got != "" {
		t.Fatalf("main till kept the old country's posture %q", got)
	}
	if got := mustSetting(t, dp, common.KeyCountry); got != "DE" {
		t.Fatalf("replica %s = %q, want the mirrored DE", common.KeyCountry, got)
	}
	if got := dp.CurrentState().Country; got != "DE" {
		t.Fatalf("replica live country = %q, want DE", got)
	}
	// The replica never resets its own fiscal state: the main till's reset
	// reaches it through the next pull.
	if got := mustSetting(t, dp, fiscal.SigningDeviceConfiguredKey("GB")); got != "true" {
		t.Fatalf("replica posture = %q, want it untouched until the pull", got)
	}
	if got := mustSetting(t, main.dp, common.KeyLocale); got != "de-DE" || mustSetting(t, dp, common.KeyLocale) != "de-DE" {
		t.Fatalf("locale main=%q replica=%q, want de-DE on both", got, mustSetting(t, dp, common.KeyLocale))
	}
	// The main till queues the new country's base plugins; this till
	// gets them with the next pull, never by installing on its own.
	wantPending(t, main.dp, specDE)
	wantPending(t, dp)
}

func TestSettingsWriteThrough_CountryUpsertGoesThroughMain(t *testing.T) {
	main, mux, dp := newCountrySyncPair(t)
	rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {common.KeyCountry}, "value": {"DE"}}, &ownerOnReplica)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("upsert store.country = %d %q", rec.Code, rec.Body.String())
	}
	if main.calls.Load() != 1 {
		t.Fatalf("main till calls = %d, want 1", main.calls.Load())
	}
	wantCountryThroughMain(t, main, dp)
	assertSettingSyncAudit(t, main.dp, "adm-1", common.KeyCountry, "DE", "Till 2")
}

func TestSettingsWriteThrough_CountrySaveGoesThroughMain(t *testing.T) {
	main, mux, dp := newCountrySyncPair(t)
	rec := postForm(mux, "/api/settings/save", url.Values{"country": {"DE"}}, &ownerOnReplica)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("save country = %d %q", rec.Code, rec.Body.String())
	}
	if main.calls.Load() != 1 {
		t.Fatalf("main till calls = %d, want 1", main.calls.Load())
	}
	wantCountryThroughMain(t, main, dp)
}

// A manager may not move a shop whose signing device is confirmed: refused,
// nothing changes on either till.
func TestSettingsWriteThrough_CountryByManagerRefusedWhileConfigured(t *testing.T) {
	main, mux, dp := newCountrySyncPair(t)
	for _, path := range []string{"/api/settings/upsert", "/api/settings/save"} {
		form := url.Values{"key": {common.KeyCountry}, "value": {"DE"}}
		if path == "/api/settings/save" {
			form = url.Values{"country": {"DE"}}
		}
		if rec := postForm(mux, path, form, &mgrUser); rec.Code != http.StatusForbidden {
			t.Fatalf("%s by a manager = %d %q, want 403", path, rec.Code, rec.Body.String())
		}
	}
	for _, d := range []*common.Deps{main.dp, dp} {
		if got := mustSetting(t, d, common.KeyCountry); got != "GB" {
			t.Fatalf("refused change moved the country to %q", got)
		}
		if got := mustSetting(t, d, fiscal.SigningDeviceConfiguredKey("GB")); got != "true" {
			t.Fatalf("refused change touched the posture: %q", got)
		}
	}
	wantPending(t, dp)
}

// The main till decides with its own posture: a replica whose copy says no
// device is confirmed still cannot let a manager move the country.
func TestSettingsWriteThrough_CountryMainTillDecidesAuthority(t *testing.T) {
	main, mux, dp := newCountrySyncPair(t)
	if err := dp.Settings.Set(t.Context(), fiscal.SigningDeviceConfiguredKey("GB"), ""); err != nil {
		t.Fatal(err)
	}
	rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {common.KeyCountry}, "value": {"DE"}}, &mgrUser)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("upsert by a manager = %d %q, want the main till's 403", rec.Code, rec.Body.String())
	}
	if main.calls.Load() != 1 {
		t.Fatalf("main till calls = %d, want 1", main.calls.Load())
	}
	if mustSetting(t, main.dp, common.KeyCountry) != "GB" || mustSetting(t, dp, common.KeyCountry) != "GB" {
		t.Fatal("a refused change moved the country")
	}
	wantPending(t, dp) // ut-docs#1068: a refused change queues no base plugins
}

// A main till still on an older version answers not_supported_via_sync:
// the operator is told to change it there, and nothing is written here.
func TestSettingsWriteThrough_CountryOlderMainPointsAtMainTill(t *testing.T) {
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeSyncOrdersJSON(w, http.StatusBadRequest, nil, syncUserError{Code: "not_supported_via_sync", Message: "store.country cannot be changed from another till yet"})
	}))
	t.Cleanup(old.Close)
	mux, dp := newSettingsSyncReplica(t, old.URL)
	if err := dp.Settings.Set(t.Context(), common.KeyCountry, "GB"); err != nil {
		t.Fatal(err)
	}
	dp.UpdateState(func(s *common.RuntimeState) { s.Country = "GB" })
	rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {common.KeyCountry}, "value": {"DE"}}, &mgrUser)
	if rec.Code < 400 || !strings.Contains(rec.Body.String(), "Change this setting on the main till.") {
		t.Fatalf("store.country = %d %q, want the change-on-main-till message", rec.Code, rec.Body.String())
	}
	if got := mustSetting(t, dp, common.KeyCountry); got != "GB" {
		t.Fatalf("replica country = %q, want GB unchanged", got)
	}
	wantPending(t, dp)
}

// ut-docs#3115 review: on an additional till the local store.name is a
// mirror that can lag the main till (a rename from my. or another till not
// pulled yet). A rename that happens to match the stale mirror must still
// reach the main till instead of being answered "unchanged" locally.
func TestSettingsWriteThrough_StoreNameForwardsEvenWhenMirrorMatches(t *testing.T) {
	main := newSettingsSyncMain(t)
	mux, dp := newSettingsSyncReplica(t, main.srv.URL)
	if err := main.dp.Settings.Set(t.Context(), common.KeyStoreName, "Renamed From Cloud"); err != nil {
		t.Fatal(err)
	}
	if err := dp.Settings.Set(t.Context(), common.KeyStoreName, "Corner Cafe"); err != nil {
		t.Fatal(err)
	}

	rec := postForm(mux, "/api/settings/store-name", url.Values{"store_name": {"Corner Cafe"}}, &mgrUser)
	if rec.Code != http.StatusNoContent && rec.Code != http.StatusOK {
		t.Fatalf("replica store-name = %d %q", rec.Code, rec.Body.String())
	}
	if main.calls.Load() != 1 {
		t.Fatalf("main till calls = %d, want 1: a rename matching the stale mirror was dropped", main.calls.Load())
	}
	if got := mustSetting(t, main.dp, common.KeyStoreName); got != "Corner Cafe" {
		t.Fatalf("main till store.name = %q, want Corner Cafe", got)
	}
	if got := mustSetting(t, dp, common.KeyStoreName); got != "Corner Cafe" {
		t.Fatalf("replica store.name = %q, want the mirrored Corner Cafe", got)
	}
	assertSettingSyncAudit(t, main.dp, "m1", common.KeyStoreName, "Corner Cafe", "Till 2")
}
