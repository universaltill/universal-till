package pages

import (
	"slices"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/fiscal"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
)

// ut-docs#2998: on a till that follows a main till, the paths below used to
// write shop-wide settings locally, where the next admin pull (main-till-wins)
// silently reverted them.

func followMainTill(t *testing.T, d *common.Deps) {
	t.Helper()
	if err := d.Settings.Set(t.Context(), "sync.primary_url", "http://primary.example"); err != nil {
		t.Fatal(err)
	}
}

// Cloud set_setting on an additional till refuses a shop-wide key and writes
// nothing -- for store.country not even the old country's fiscal posture
// reset. The cloud already withholds set_setting from satellites
// (ut-docs#2633); this is the till's own fail-closed half.
func TestCloudSetSetting_AdditionalTillRefusesShopWideKeys(t *testing.T) {
	_, _, d := newFullAuthDeps(t)
	ctx := t.Context()
	if err := d.Settings.Set(ctx, common.KeyCountry, "DE"); err != nil {
		t.Fatal(err)
	}
	d.UpdateState(func(s *common.RuntimeState) { s.Country = "DE" })
	configuredKey := fiscal.SigningDeviceConfiguredKey("DE")
	if err := d.Settings.Set(ctx, configuredKey, "true"); err != nil {
		t.Fatal(err)
	}
	followMainTill(t, d)
	hooks := buildCloudHooks(d, nil)

	for _, c := range []struct{ key, value string }{
		{"store.name", "Renamed on a replica"},
		{common.KeyCountry, "ES"},
		{"receipt.footer", "x"},
		{"no.such.family", "x"}, // unclassified: never written locally either
	} {
		if _, err := hooks.SetSetting(ctx, c.key, c.value); err == nil {
			t.Fatalf("SetSetting(%s) on an additional till: want a refusal, got nil", c.key)
		}
		if v, _, _ := d.Settings.Get(ctx, c.key); v == c.value {
			t.Fatalf("SetSetting(%s) refused but still wrote %q locally", c.key, v)
		}
	}
	if v, _, _ := d.Settings.Get(ctx, configuredKey); v != "true" {
		t.Fatalf("%s = %q after a refused store.country directive, want the posture untouched (true)", configuredKey, v)
	}
	wantPending(t, d)
}

// A per-till key still applies on an additional till, and a main till still
// applies a shop-wide one.
func TestCloudSetSetting_PerTillKeyOnAdditionalTill_ShopWideOnMainTill(t *testing.T) {
	_, _, d := newFullAuthDeps(t)
	ctx := t.Context()

	if _, err := buildCloudHooks(d, nil).SetSetting(ctx, "store.name", "Main Street"); err != nil {
		t.Fatalf("main till SetSetting(store.name): %v", err)
	}
	if v, _, _ := d.Settings.Get(ctx, "store.name"); v != "Main Street" {
		t.Fatalf("main till store.name = %q, want Main Street", v)
	}

	followMainTill(t, d)
	if _, err := buildCloudHooks(d, nil).SetSetting(ctx, data.ThemeSettingsKey, "dark"); err != nil {
		t.Fatalf("additional till SetSetting(%s) (per-till): %v", data.ThemeSettingsKey, err)
	}
	if v, _, _ := d.Settings.Get(ctx, data.ThemeSettingsKey); v != "dark" {
		t.Fatalf("%s = %q, want dark", data.ThemeSettingsKey, v)
	}
}

// set_till_setting: the shop-wide whitelisted keys are refused on an
// additional till; printer.receipt_policy (per-till) still applies.
func TestCloudSetTillSetting_AdditionalTillRefusesShopWideKeys(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	followMainTill(t, dp)

	for key := range allowedRemoteTillSettingKeys {
		if data.SettingScope(key) == data.SettingPerTill {
			continue
		}
		if _, err := cloudSetTillSetting(ctx, dp, nil, key, "x"); err == nil {
			t.Fatalf("set_till_setting %s on an additional till: want a refusal, got nil", key)
		}
		if v, _, _ := dp.Settings.Get(ctx, key); v == "x" {
			t.Fatalf("set_till_setting %s refused but still wrote locally", key)
		}
	}
	if _, err := cloudSetTillSetting(ctx, dp, nil, keyPrinterReceiptPolicy, "ask"); err != nil {
		t.Fatalf("set_till_setting %s (per-till) on an additional till: %v", keyPrinterReceiptPolicy, err)
	}
	if v, _, _ := dp.Settings.Get(ctx, keyPrinterReceiptPolicy); v != "ask" {
		t.Fatalf("%s = %q, want ask", keyPrinterReceiptPolicy, v)
	}
}

// The base-plugin install-retry queue is this till's own: per-till, never
// carried by the admin bundle.
func TestPendingBasePlugins_IsPerTill(t *testing.T) {
	if got := data.SettingScope(common.KeyPendingBasePlugins); got != data.SettingPerTill {
		t.Fatalf("SettingScope(%s) = %v, want SettingPerTill", common.KeyPendingBasePlugins, got)
	}
}

// A language pack installed on an additional till never derives store.locale
// there: the main till's locale arrives with the next pull.
func TestResolveAndInstallBasePlugin_AdditionalTillKeepsLocale(t *testing.T) {
	dp := newBasePluginTestDeps(t)
	dp.UpdateState(func(s *common.RuntimeState) { s.Country = "PK" })
	newHermeticEnOnlyI18n(t, dp)
	followMainTill(t, dp)
	before := dp.CurrentState().Locale

	mkt := newFakeMarketplace(t, nil)
	mkt.publishLanguageVersion(t, "listing-lang-ur", "ut-plugin-language-ur", "1.0.0", "ur", []byte(`{"nav.home":"صفحہ اول"}`))
	mkt.setCatalog(marketplace.PluginSummary{
		ID: "ut-plugin-language-ur", ListingID: "listing-lang-ur", Name: "Urdu language pack",
		Version: "1.0.0", CanonicalType: "language", AvailableLocales: []string{"ur"},
	})
	dp.Cfg.Marketplace = mkt.config()

	if err := resolveAndInstallBasePlugin(t.Context(), dp, basePluginSpec{CanonicalType: "language", Locale: "ur"}); err != nil {
		t.Fatalf("resolveAndInstallBasePlugin: %v", err)
	}
	if !slices.Contains(httpx.AvailableLocales(), "ur") {
		t.Fatal("ur not available after install -- the test proves nothing past this point")
	}
	if got := dp.CurrentState().Locale; got != before {
		t.Fatalf("store.locale = %q on an additional till, want it unchanged (%q)", got, before)
	}
	if v, _, _ := dp.Settings.Get(t.Context(), common.KeyLocale); v == "ur-PK" {
		t.Fatalf("store.locale persisted as %q on an additional till", v)
	}
}

// The boot backfill of store.locale_confirmed does not run on an additional
// till: the main till's value wins at the next pull.
func TestBackfillLocaleConfirmed_AdditionalTillNoOp(t *testing.T) {
	dp := newBasePluginTestDeps(t)
	dp.UpdateState(func(s *common.RuntimeState) { s.Country = "PK"; s.Locale = "de-AT" })
	if err := common.SaveState(t.Context(), dp.Settings, dp.CurrentState()); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	if err := savePendingBasePlugins(t.Context(), dp, []basePluginSpec{{CanonicalType: "language", Locale: "ur"}}); err != nil {
		t.Fatalf("seed pending base plugins: %v", err)
	}
	followMainTill(t, dp)

	backfillLocaleConfirmedForDivergedPendingTills(t.Context(), dp)

	if v, _, _ := dp.Settings.Get(t.Context(), common.KeyLocaleConfirmed); v == "true" {
		t.Fatalf("%s backfilled on an additional till", common.KeyLocaleConfirmed)
	}
}
