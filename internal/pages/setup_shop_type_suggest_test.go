package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
)

// ut-docs#3706 (ADR-0149 §8, item 5 of the 2026-11-28 amendment): the setup
// wizard's shop-type step suggests an optional plugin driven ONLY by the
// neutral `shop_types` field the catalogue carries (ut-docs#7246) — core never
// names a plugin. Nothing installs unless the operator ticks the box, and an
// untouched suggestion leaves no trace.

const (
	stListing = "listing-st-service"
	stPlugin  = "com.example.st-service"
)

func resetShopTypeSuggestForTest(t *testing.T) {
	t.Helper()
	resetSetupShopTypeSuggest()
	t.Cleanup(resetSetupShopTypeSuggest)
}

func stEntry(listing, plugin, version string, types ...string) marketplace.PluginSummary {
	return marketplace.PluginSummary{
		ID: listing, ListingID: listing, Name: "Suggested " + plugin, Description: "A helper for " + plugin,
		Version: version, CanonicalType: "page", ShopTypes: types,
	}
}

func TestShopTypeSuggestionFor_PicksHighestSemverAndFilters(t *testing.T) {
	paid := stEntry("p", "p", "9.0.0", "service")
	paid.PaidListing = true
	tax := stEntry("t", "t", "9.0.0", "service")
	tax.CanonicalType = "tax"
	entries := []marketplace.PluginSummary{
		stEntry("a-old", "a", "1.0.0", "service"),
		stEntry("a-new", "a", "1.2.0", "service", "retail"),
		stEntry("other", "o", "5.0.0", "retail"), // does not name service: a server that ignores the filter must not leak it
		stEntry("silent", "s", "5.0.0"),
		paid, tax,
	}
	got := shopTypeSuggestionFor(entries, "service")
	if got == nil || got.ListingID != "a-new" {
		t.Fatalf("want the highest-semver free non-fiscal listing naming service, got %+v", got)
	}
	if shopTypeSuggestionFor(entries, "cafe") != nil {
		t.Fatal("no listing names cafe: no suggestion")
	}
	if shopTypeSuggestionFor(entries, "restaurant") != nil || shopTypeSuggestionFor(entries, "") != nil {
		t.Fatal("an unknown or empty shop type never suggests")
	}
}

func TestSetupShopTypeSuggestions_FromCatalogue(t *testing.T) {
	resetShopTypeSuggestForTest(t)
	dp := newBasePluginTestDeps(t)
	mkt := newFakeMarketplace(t, map[string]string{stListing: stPlugin})
	mkt.setCatalog(stEntry(stListing, stPlugin, "1.0.0", "service"))
	dp.Cfg.Marketplace = mkt.config()

	got := setupShopTypeSuggestions(t.Context(), dp)
	if len(got) != 1 || got["service"] == nil || got["service"].ListingID != stListing {
		t.Fatalf("suggestions = %+v, want exactly service -> %s", got, stListing)
	}
}

func TestSetupShopTypeSuggestions_UnreachableOrEmptyIsSilent(t *testing.T) {
	resetShopTypeSuggestForTest(t)
	dp := newBasePluginTestDeps(t)
	dp.Cfg.Marketplace.EndpointURL = "" // no marketplace
	if got := setupShopTypeSuggestions(t.Context(), dp); len(got) != 0 {
		t.Fatalf("no marketplace: no suggestions, got %+v", got)
	}
	resetShopTypeSuggestForTest(t)
	mkt := newFakeMarketplace(t, map[string]string{})
	dp.Cfg.Marketplace = mkt.config()
	if got := setupShopTypeSuggestions(t.Context(), dp); len(got) != 0 {
		t.Fatalf("empty catalogue: no suggestions, got %+v", got)
	}
}

func TestSetupShopTypeSuggestions_AlreadyInstalledIsNotSuggested(t *testing.T) {
	resetShopTypeSuggestForTest(t)
	dp := newBasePluginTestDeps(t)
	mkt := newFakeMarketplace(t, map[string]string{stListing: stPlugin})
	mkt.publishVersion(t, stListing, stPlugin, "1.0.0")
	mkt.setCatalog(stEntry(stListing, stPlugin, "1.0.0", "service"))
	dp.Cfg.Marketplace = mkt.config()
	if _, err := cloudInstallPluginVersion(t.Context(), dp, stListing, "1.0.0"); err != nil {
		t.Fatalf("seed install: %v", err)
	}
	if got := setupShopTypeSuggestions(t.Context(), dp); len(got) != 0 {
		t.Fatalf("an installed listing must not be suggested again, got %+v", got)
	}
}

func getSetupPage(t *testing.T, mux *http.ServeMux) string {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/setup", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /setup: %d", rec.Code)
	}
	return rec.Body.String()
}

func TestSetupPage_ShowsOptionalSuggestionOnShopTypeStep(t *testing.T) {
	resetShopTypeSuggestForTest(t)
	resetTaxCatalogForTest(t)
	withOSLocale(t, "", "") // hermetic against the machine's OS locale (ut-docs#662)
	mux, dp := newRealDBDeps(t)
	initTestPaths(t)
	mkt := newFakeMarketplace(t, map[string]string{stListing: stPlugin})
	mkt.setCatalog(stEntry(stListing, stPlugin, "1.0.0", "service"))
	dp.Cfg.Marketplace = mkt.config()

	body := getSetupPage(t, mux)
	if !strings.Contains(body, `data-shop-type-suggestion="service"`) {
		t.Fatalf("the service suggestion block is missing from the shop-type step")
	}
	if !strings.Contains(body, `name="suggest_plugin" value="`+stListing+`"`) {
		t.Fatal("the opt-in checkbox for the listing is missing")
	}
	if strings.Contains(body, `name="suggest_plugin" value="`+stListing+`" checked`) {
		t.Fatal("the suggestion must never be pre-ticked")
	}
	if !strings.Contains(body, "Suggested "+stPlugin) {
		t.Fatal("the listing's own name must be shown (core does not name plugins)")
	}

	// Nothing published for any shop type: the step is unchanged.
	resetShopTypeSuggestForTest(t)
	mkt.setCatalog()
	if body := getSetupPage(t, mux); strings.Contains(body, "data-shop-type-suggestion") {
		t.Fatal("no listing names a shop type: no suggestion block")
	}
}

func setupForm(extra url.Values) url.Values {
	f := url.Values{
		"pin": {"2468"}, "pin_confirm": {"2468"}, "country": {"GB"}, "currency": {"GBP"},
		"store_name": {"Corner Cafe"}, "till_name": {"Till 1"}, "shop_type": {"service"},
	}
	for k, v := range extra {
		f[k] = v
	}
	return f
}

func newSuggestSetup(t *testing.T) (*http.ServeMux, func(url.Values) int) {
	t.Helper()
	resetShopTypeSuggestForTest(t)
	resetTaxCatalogForTest(t)
	mux, dp := newRealDBDeps(t)
	initTestPaths(t)
	mkt := newFakeMarketplace(t, map[string]string{stListing: stPlugin})
	mkt.publishVersion(t, stListing, stPlugin, "1.0.0")
	mkt.setCatalog(stEntry(stListing, stPlugin, "1.0.0", "service"))
	dp.Cfg.Marketplace = mkt.config()
	plugins.SharedBus(dp.Db).ResetSubscribers()
	return mux, func(extra url.Values) int {
		rec := postForm(mux, "/api/setup", setupForm(extra), nil)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("setup POST: %d %.200s", rec.Code, rec.Body.String())
		}
		active, err := data.NewPluginRepo(dp.Db).PluginActive(t.Context(), stPlugin)
		if err != nil {
			t.Fatal(err)
		}
		if active {
			return 1
		}
		if pending, _ := loadPendingBasePlugins(t.Context(), dp); len(withoutAutoTax(pending)) != 0 {
			t.Fatalf("a suggestion must never leave a pending entry behind, got %+v", pending)
		}
		return 0
	}
}

func TestSetupPOST_TickedSuggestionInstalls(t *testing.T) {
	_, post := newSuggestSetup(t)
	if post(url.Values{"suggest_plugin": {stListing}}) != 1 {
		t.Fatal("the ticked suggestion must install")
	}
}

// Skipping leaves no trace: nothing installed, nothing queued, no setting.
func TestSetupPOST_UntickedSuggestionLeavesNoTrace(t *testing.T) {
	_, post := newSuggestSetup(t)
	if post(nil) != 0 {
		t.Fatal("an untouched suggestion must install nothing")
	}
}

// The posted id is never trusted: it must be the suggestion the catalogue
// gives for the shop type that is saved.
func TestSetupPOST_SuggestionForAnotherTypeOrForgedIDInstallsNothing(t *testing.T) {
	_, post := newSuggestSetup(t)
	if post(url.Values{"shop_type": {"retail"}, "suggest_plugin": {stListing}}) != 0 {
		t.Fatal("the service suggestion must not install for a retail shop")
	}
	_, post = newSuggestSetup(t)
	if post(url.Values{"suggest_plugin": {"listing-forged"}}) != 0 {
		t.Fatal("a listing id the catalogue did not suggest must not install")
	}
}
