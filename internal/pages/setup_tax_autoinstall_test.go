package pages

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
)

// ADR-0172 §1, §3, §4 (ut-docs#6288): setup queues the shop country's tax
// plugin for BOTH a till joined to a my. shop and a till set up with the
// wizard alone, attempts it once (5 s), and leaves the rest to the retry.
// Against the mock marketplace (fakeMarketplace) with the same eligibility
// rules the real ut-plugin-tax-uk manifest is judged by (ut-docs#6285 tests).

const (
	gbTaxListing  = "listing-tax-gb"
	gbTaxPluginID = "com.universaltill.tax-gb"
)

// gbTaxManifest mirrors the shape of the UK plugin the mock marketplace
// serves: first-party id, `markets: [GB]`, a tax entry, the tax.rate.ask hook,
// allow-listed permissions only (no net:, no register scope).
func gbTaxManifest() *plugins.Manifest {
	return &plugins.Manifest{
		ID: gbTaxPluginID, Name: "UK VAT", Version: "1.4.0",
		Markets:     []string{"GB"},
		Hooks:       []plugins.ManifestHook{{Event: "tax.rate.ask", Action: "tax.rate"}},
		Entries:     []plugins.ManifestEntry{{Type: "tax", Key: "eatin-standard-rate", Label: "UK eat-in VAT"}},
		Permissions: []string{"events:receive", "sales:read", "storage"},
	}
}

func gbTaxCatalogEntry() marketplace.PluginSummary {
	return marketplace.PluginSummary{
		ID: gbTaxListing, ListingID: gbTaxListing, Name: "UK VAT", Version: "1.4.0",
		CanonicalType: "tax", Markets: []string{"GB"}, TrustTier: "official",
		Permissions: []string{"events:receive", "sales:read", "storage"},
	}
}

func publishGBTax(t *testing.T, mkt *fakeMarketplace) {
	t.Helper()
	// The decline guest: the plugin's default empty mapping declines every
	// line (ADR-0172 §7), so the first-load check proves "loads and answers".
	mkt.publishCustomTaxManifest(t, gbTaxListing, gbTaxManifest(), buildFiscalGuest(t, "taxdecline_guest"))
	mkt.setCatalog(gbTaxCatalogEntry())
}

const gbCloudStoreAnswer = `{"data":{"store_id":"` + pairPageStore + `","merchant_id":"merchant-1","device_id":"x","device_token":"` + pairPageToken + `",` +
	`"store":{"name":"Cloud Cafe","country":"GB","currency":"GBP","currency_decimals":2,"catalog_master":"cloud"}},"error":null}`

// TestSetupGBJoin_AutoInstallsEligibleTaxPlugin is the #4837 acceptance
// test: a GB till that joins a my. shop ends setup with the eligible tax
// plugin installed and active, and without a click. The country is the pair
// response's (server-held); the form's posted country is ignored on this
// path (finalCloudPost posts a tampered one).
func TestSetupGBJoin_AutoInstallsEligibleTaxPlugin(t *testing.T) {
	resetTaxCatalogForTest(t)
	mux, d, stub, nonce := cloudWizard(t)
	stub.set(200, gbCloudStoreAnswer)
	mkt := newFakeMarketplace(t, map[string]string{})
	publishGBTax(t, mkt)
	plugins.SharedBus(d.Db).ResetSubscribers()
	initTestPaths(t)

	doCloudPair(mux, nonce, nil)
	d.Cfg.Marketplace = mkt.config() // the plugin catalogue and downloads
	rec := finalCloudPost(mux, nonce, func(v url.Values) { v.Set("country", "DE") })
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("join setup: code=%d body=%.300s", rec.Code, rec.Body.String())
	}

	if active, err := data.NewPluginRepo(d.Db).PluginActive(t.Context(), gbTaxPluginID); err != nil || !active {
		t.Fatalf("a GB till joined to a my. shop must end with the UK tax plugin active: active=%v err=%v", active, err)
	}
	if pending, _ := loadPendingBasePlugins(t.Context(), d); len(withoutAutoTax(pending)) != 0 || len(pending) != 0 {
		t.Fatalf("nothing may stay pending after a successful install, got %+v", pending)
	}
	if st := loadTaxAutoInstallStatus(t.Context(), d); st == nil || st.State != taxStateInstalled || st.PluginID != gbTaxPluginID {
		t.Fatalf("the chip must say installed, got %+v", st)
	}
}

// An offline join finishes setup with the spec pending; the retry installs
// it once the till is online (ADR-0172 §3/§4).
func TestSetupGBJoin_OfflineFinishesPendingThenRetryInstalls(t *testing.T) {
	resetTaxCatalogForTest(t)
	mux, d, stub, nonce := cloudWizard(t)
	stub.set(200, gbCloudStoreAnswer)
	mkt := newFakeMarketplace(t, map[string]string{})
	publishGBTax(t, mkt)
	plugins.SharedBus(d.Db).ResetSubscribers()
	initTestPaths(t)

	doCloudPair(mux, nonce, nil)
	d.Cfg.Marketplace.EndpointURL = unreachableURL() // offline from here on
	rec := finalCloudPost(mux, nonce, nil)
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/import?welcome=1") {
		t.Fatalf("setup must finish offline: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
	spec := basePluginSpec{CanonicalType: "tax", Market: "GB"}
	pending, _ := loadPendingBasePlugins(t.Context(), d)
	if !containsSpec(pending, spec) {
		t.Fatalf("the tax spec must be pending, got %+v", pending)
	}
	if st := loadTaxAutoInstallStatus(t.Context(), d); st == nil || st.State != taxStateRetrying || st.ErrorClass != taxErrOffline {
		t.Fatalf("the chip must say retrying/offline, got %+v", st)
	}
	if active, _ := data.NewPluginRepo(d.Db).PluginActive(t.Context(), gbTaxPluginID); active {
		t.Fatal("nothing may install while offline")
	}

	d.Cfg.Marketplace = mkt.config() // back online
	basePluginRetryTick(t.Context(), d)
	if active, _ := data.NewPluginRepo(d.Db).PluginActive(t.Context(), gbTaxPluginID); !active {
		t.Fatal("the retry must install the plugin once online")
	}
	if pending, _ := loadPendingBasePlugins(t.Context(), d); containsSpec(pending, spec) {
		t.Fatalf("the spec must leave the pending list once installed, got %+v", pending)
	}
}

// A wizard-only till (no my. shop) gets the same behaviour from the
// operator-confirmed country (ADR-0172 §1(b), owner question 1).
func TestSetupWizardOnlyGB_AutoInstallsTaxPlugin(t *testing.T) {
	resetTaxCatalogForTest(t)
	mux, dp := newRealDBDeps(t)
	initTestPaths(t)
	mkt := newFakeMarketplace(t, map[string]string{})
	publishGBTax(t, mkt)
	dp.Cfg.Marketplace = mkt.config()
	plugins.SharedBus(dp.Db).ResetSubscribers()

	rec := postForm(mux, "/api/setup", url.Values{
		"pin": {"2468"}, "pin_confirm": {"2468"}, "country": {"GB"}, "currency": {"GBP"},
		"store_name": {"Corner Cafe"}, "till_name": {"Till 1"},
	}, nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("wizard setup: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if active, _ := data.NewPluginRepo(dp.Db).PluginActive(t.Context(), gbTaxPluginID); !active {
		t.Fatal("a wizard-only GB till must also get the UK tax plugin")
	}
}

// A DE join installs nothing automatically (the German plugin signs and
// asks for a register scope: never eligible) and the wizard still shows the
// prompted tile for DE.
func TestSetupDEJoin_InstallsNothingAndPromptsTile(t *testing.T) {
	resetTaxCatalogForTest(t)
	mux, d, _, nonce := cloudWizard(t) // the default pair answer is DE
	mkt := newFakeMarketplace(t, map[string]string{})
	de := &plugins.Manifest{
		ID: "com.universaltill.tax-de", Name: "German fiscal", Version: "2.0.0", Markets: []string{"DE"},
		Hooks: []plugins.ManifestHook{
			{Event: "tax.rate.ask", Action: "tax.rate"},
			{Event: "fiscal.sign.ask", Action: "sign"},
		},
		Entries:     []plugins.ManifestEntry{{Type: "tax", Key: "de-switch", Label: "DE"}},
		Permissions: []string{"events:receive", "fiscal_register_de:read"},
		Provides:    []string{"fiscal.register"},
	}
	mkt.publishCustomTaxManifest(t, "listing-tax-de", de, buildFiscalGuest(t, "taxdecline_guest"))
	mkt.setCatalog(marketplace.PluginSummary{
		ID: "listing-tax-de", ListingID: "listing-tax-de", Name: "German fiscal", Version: "2.0.0",
		CanonicalType: "tax", Markets: []string{"DE"}, TrustTier: "official",
		Permissions: []string{"events:receive", "fiscal_register_de:read"},
	})
	initTestPaths(t)

	doCloudPair(mux, nonce, nil)
	d.Cfg.Marketplace = mkt.config()
	if rec := finalCloudPost(mux, nonce, nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("join setup: code=%d", rec.Code)
	}
	if active, _ := data.NewPluginRepo(d.Db).PluginActive(t.Context(), "com.universaltill.tax-de"); active {
		t.Fatal("a fiscal (signing) plugin must never be auto-installed")
	}
	if hits := mkt.downloadTokenHits(); hits != 0 {
		t.Fatalf("an ineligible listing must not even be downloaded, got %d download-token hits", hits)
	}
	if st := loadTaxAutoInstallStatus(t.Context(), d); st != nil {
		t.Fatalf("no chip when nothing is auto-installed, got %+v", st)
	}
	resetSetupTaxCatalog() // the wizard's own render cached a miss against the pairing stub
	tile, _ := setupInstallableTaxPlugin(t.Context(), d, "DE")
	if tile == nil || tile.ListingID != "listing-tax-de" {
		t.Fatalf("the prompted tile must be offered for DE, got %+v", tile)
	}
}

// Auto-installable country: no prompted tile and no step 3 for it (the
// plugin installs itself); a country with a prompted listing keeps its tile.
func TestWizardTaxStepFollowsTheCatalog(t *testing.T) {
	resetTaxCatalogForTest(t)
	dp := newBasePluginTestDeps(t)
	mkt := newFakeMarketplace(t, map[string]string{})
	publishGBTax(t, mkt)
	dp.Cfg.Marketplace = mkt.config()

	if tile, _ := setupInstallableTaxPlugin(t.Context(), dp, "GB"); tile != nil {
		t.Fatalf("an eligible plugin needs no tile, got %+v", tile)
	}
	for _, tl := range setupInstallableTaxPlugins(t.Context(), dp) {
		if tl.Country == "GB" {
			t.Fatalf("GB must not get a step-3 tile: %+v", tl)
		}
	}
	// Two eligible listings: the till never picks, the prompt stays.
	second := gbTaxCatalogEntry()
	second.ID, second.ListingID, second.Name = "listing-tax-gb-2", "listing-tax-gb-2", "Other UK VAT"
	mkt.setCatalog(gbTaxCatalogEntry(), second)
	resetSetupTaxCatalog()
	if tile, _ := setupInstallableTaxPlugin(t.Context(), dp, "GB"); tile == nil {
		t.Fatal("two eligible listings must keep the prompted tile")
	}
}

// Setup never queues the tax plugin when the country is later changed in
// Settings: only setup completion does (ADR-0172 §1).
func TestCountryChangeDoesNotQueueTaxPlugin(t *testing.T) {
	dp := newBasePluginTestDeps(t)
	drainBasePluginRetryNudge()
	queueBasePluginsForCountryChange(t.Context(), dp, "GB")
	pending, _ := loadPendingBasePlugins(t.Context(), dp)
	if got := len(pending); got != 0 {
		t.Fatalf("a later country change must not queue the tax plugin, got %+v", pending)
	}
}

func containsSpec(list []basePluginSpec, s basePluginSpec) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
