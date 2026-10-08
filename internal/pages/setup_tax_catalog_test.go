package pages

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
)

// ut-docs#1180: ADR-0025 decision 4 — a fiscal ("tax") plugin like
// ut-plugin-tax-de must be PROMPTED at wizard time, never silently
// auto-installed. setupInstallableTaxPlugin resolves whether the wizard's
// current country has an installable-and-not-yet-installed tax plugin;
// setupTaxPluginInstallHandler is the explicit-click install action.

// resetTaxCatalogForTest isolates each test from the package-level TTL cache
// — mirrors resetLangCatalogForTest.
func resetTaxCatalogForTest(t *testing.T) {
	t.Helper()
	resetSetupTaxCatalog()
	t.Cleanup(resetSetupTaxCatalog)
}

// deTaxCatalogEntry is the DE fiscal-plugin listing a real marketplace would
// serve: canonical type "tax", availableLocales ["de"] — mirrors
// deLanguageCatalogEntry (setup_base_plugins_test.go) for the tax capability.
func deTaxCatalogEntry(listingID, pluginID, version string) marketplace.PluginSummary {
	return marketplace.PluginSummary{
		ID: pluginID, ListingID: listingID, Name: "German fiscal plugin",
		Version: version, CanonicalType: "tax", AvailableLocales: []string{"de"},
	}
}

// --- setupInstallableTaxPlugin ---

func TestSetupInstallableTaxPlugin_DEMatchesCatalog(t *testing.T) {
	resetTaxCatalogForTest(t)
	dp := newBasePluginTestDeps(t)
	mkt := newFakeMarketplace(t, map[string]string{"listing-tax-de": "ut-plugin-tax-de"})
	mkt.setCatalog(deTaxCatalogEntry("listing-tax-de", "ut-plugin-tax-de", "1.0.0"))
	dp.Cfg.Marketplace = mkt.config()

	plugin, unavailable := setupInstallableTaxPlugin(t.Context(), dp, "DE")
	if unavailable {
		t.Fatal("catalog was reachable, must not report unavailable")
	}
	if plugin == nil {
		t.Fatal("expected a DE tax plugin match, got nil")
	}
	if plugin.Country != "DE" || plugin.ListingID != "listing-tax-de" {
		t.Fatalf("plugin = %+v, want Country=DE ListingID=listing-tax-de", plugin)
	}
}

// A country with nothing in countryTaxLocale (this is a deliberately minimal
// table — not every country) returns nil, false: nothing to prompt, but NOT
// "catalog unavailable" (which would show a misleading note).
func TestSetupInstallableTaxPlugin_UnmappedCountryReturnsNilNotUnavailable(t *testing.T) {
	resetTaxCatalogForTest(t)
	dp := newBasePluginTestDeps(t)
	mkt := newFakeMarketplace(t, map[string]string{"listing-tax-de": "ut-plugin-tax-de"})
	mkt.setCatalog(deTaxCatalogEntry("listing-tax-de", "ut-plugin-tax-de", "1.0.0"))
	dp.Cfg.Marketplace = mkt.config()

	plugin, unavailable := setupInstallableTaxPlugin(t.Context(), dp, "US")
	if plugin != nil {
		t.Fatalf("expected no match for an unmapped country, got %+v", plugin)
	}
	if unavailable {
		t.Fatal("an unmapped country is not a catalog-unavailable case")
	}
}

// Once the matching listing is already installed and active, the tile must
// disappear — this is what makes the wizard stop prompting after a real
// install.
func TestSetupInstallableTaxPlugin_AlreadyActiveReturnsNil(t *testing.T) {
	resetTaxCatalogForTest(t)
	dp := newBasePluginTestDeps(t)
	mkt := newFakeMarketplace(t, map[string]string{"listing-tax-de": "ut-plugin-tax-de"})
	mkt.setCatalog(deTaxCatalogEntry("listing-tax-de", "ut-plugin-tax-de", "1.0.0"))
	dp.Cfg.Marketplace = mkt.config()

	spec := basePluginSpec{CanonicalType: "tax", Locale: "de"}
	if err := resolveAndInstallBasePlugin(t.Context(), dp, spec); err != nil {
		t.Fatalf("seed install: %v", err)
	}
	if active, err := data.NewPluginRepo(dp.Db).PluginActive(t.Context(), "ut-plugin-tax-de"); err != nil || !active {
		t.Fatalf("seed install did not land: active=%v err=%v", active, err)
	}

	plugin, unavailable := setupInstallableTaxPlugin(t.Context(), dp, "DE")
	if plugin != nil {
		t.Fatalf("expected nil once the listing is already active, got %+v", plugin)
	}
	if unavailable {
		t.Fatal("an already-installed plugin is not a catalog-unavailable case")
	}
}

// Among several matching listings, the highest semver wins (mirrors
// TestResolveAndInstallBasePlugin_PicksHighestSemverVersion).
func TestSetupInstallableTaxPlugin_PicksHighestSemverVersion(t *testing.T) {
	resetTaxCatalogForTest(t)
	dp := newBasePluginTestDeps(t)
	mkt := newFakeMarketplace(t, map[string]string{
		"listing-tax-de-old": "ut-plugin-tax-de-old",
		"listing-tax-de-new": "ut-plugin-tax-de-new",
	})
	mkt.publishVersion(t, "listing-tax-de-old", "ut-plugin-tax-de-old", "1.0.0")
	mkt.publishVersion(t, "listing-tax-de-new", "ut-plugin-tax-de-new", "1.2.0")
	mkt.setCatalog(
		deTaxCatalogEntry("listing-tax-de-old", "ut-plugin-tax-de-old", "1.0.0"),
		deTaxCatalogEntry("listing-tax-de-new", "ut-plugin-tax-de-new", "1.2.0"),
	)
	dp.Cfg.Marketplace = mkt.config()

	plugin, _ := setupInstallableTaxPlugin(t.Context(), dp, "DE")
	if plugin == nil || plugin.ListingID != "listing-tax-de-new" {
		t.Fatalf("expected the higher-semver listing, got %+v", plugin)
	}
}

// A non-tax listing (e.g. a language pack) declaring the de locale must
// never surface as a tax-plugin tile — CanonicalType must match exactly.
func TestSetupInstallableTaxPlugin_IgnoresNonTaxListings(t *testing.T) {
	resetTaxCatalogForTest(t)
	dp := newBasePluginTestDeps(t)
	mkt := newFakeMarketplace(t, map[string]string{"listing-lang-de": "ut-plugin-language-de"})
	mkt.setCatalog(deLanguageCatalogEntry("listing-lang-de", "ut-plugin-language-de", "1.0.0"))
	dp.Cfg.Marketplace = mkt.config()

	plugin, _ := setupInstallableTaxPlugin(t.Context(), dp, "DE")
	if plugin != nil {
		t.Fatalf("a language listing must never match the tax prompt, got %+v", plugin)
	}
}

// deadMarketplace points dp at a marketplace that refuses connections (a
// closed local server fails immediately, no timeout wait) — a fully offline
// first boot with nothing cached.
func deadMarketplace(t *testing.T, dp *common.Deps) {
	t.Helper()
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	dp.Cfg.Marketplace.EndpointURL = dead.URL
	dp.Cfg.Marketplace.ClientID = "merchant-1"
	dp.Cfg.Marketplace.StoreID = "store-1"
	dead.Close()
}

// Catalog unreachable with nothing cached: catalogUnavailable=true (distinct
// from the unmapped-country case above). ut-docs#1512 (owner decision
// 2026-09-28, "always show the prompt"): a tax-mapped country still gets a
// tile — in its Offline state, with no listing to name yet — so a fully
// offline German first boot prompts the operator instead of silently showing
// nothing (which also left #1506's queue-on-Skip/Next with no tile to hang
// off).
func TestSetupInstallableTaxPlugin_CatalogUnreachableReturnsOfflineTile(t *testing.T) {
	resetTaxCatalogForTest(t)
	dp := newBasePluginTestDeps(t)
	deadMarketplace(t, dp)

	plugin, unavailable := setupInstallableTaxPlugin(t.Context(), dp, "de")
	if !unavailable {
		t.Fatal("expected catalogUnavailable=true when the catalog cannot be reached and nothing is cached")
	}
	if plugin == nil {
		t.Fatal("ut-docs#1512: an offline DE first boot must still get a (offline) tax-plugin tile, got nil")
	}
	if !plugin.Offline || plugin.Country != "DE" || plugin.ListingID != "" {
		t.Fatalf("plugin = %+v, want Offline=true Country=DE ListingID=\"\"", plugin)
	}
}

// An unmapped country stays tile-less offline too — the offline tile is for
// countries that need a fiscal plugin, not every country.
func TestSetupInstallableTaxPlugin_CatalogUnreachableUnmappedCountryReturnsNil(t *testing.T) {
	resetTaxCatalogForTest(t)
	dp := newBasePluginTestDeps(t)
	deadMarketplace(t, dp)

	if plugin, _ := setupInstallableTaxPlugin(t.Context(), dp, "FR"); plugin != nil {
		t.Fatalf("FR has no fiscal plugin mapped, want nil even offline, got %+v", plugin)
	}
}

// Once the catalog HAS been reached, the online tile is unchanged: a
// reachable catalog with no DE tax listing still means no tile.
func TestSetupInstallableTaxPlugin_ReachableCatalogWithoutListingIsNotOffline(t *testing.T) {
	resetTaxCatalogForTest(t)
	dp := newBasePluginTestDeps(t)
	mkt := newFakeMarketplace(t, map[string]string{})
	dp.Cfg.Marketplace = mkt.config()

	if plugin, unavailable := setupInstallableTaxPlugin(t.Context(), dp, "DE"); plugin != nil || unavailable {
		t.Fatalf("reachable catalog with no DE listing: plugin=%+v unavailable=%v, want nil,false", plugin, unavailable)
	}
}

// --- setupTaxPluginInstallHandler (POST /api/setup/tax-plugin) ---

func TestSetupTaxPluginInstallHappyPath(t *testing.T) {
	resetTaxCatalogForTest(t)
	mux, dp := newRealDBDeps(t)
	initTestPaths(t)
	mkt := newFakeMarketplace(t, map[string]string{"listing-tax-de": "ut-plugin-tax-de"})
	mkt.setCatalog(deTaxCatalogEntry("listing-tax-de", "ut-plugin-tax-de", "1.0.0"))
	dp.Cfg.Marketplace = mkt.config()

	// The redirect carries the country back so the wizard resumes on step 3
	// (where the tile lives) instead of restarting at step 1.
	rec := postForm(mux, "/api/setup/tax-plugin", url.Values{"country": {"DE"}}, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/setup?tax_country=DE" {
		t.Fatalf("POST /api/setup/tax-plugin: code=%d loc=%q, want 303 -> /setup?tax_country=DE",
			rec.Code, rec.Header().Get("Location"))
	}
	if active, err := data.NewPluginRepo(dp.Db).PluginActive(t.Context(), "ut-plugin-tax-de"); err != nil || !active {
		t.Fatalf("expected the tax plugin installed: active=%v err=%v", active, err)
	}
	if pending, _ := loadPendingBasePlugins(t.Context(), dp); len(pending) != 0 {
		t.Fatalf("nothing should be pending after a successful foreground install, got %+v", pending)
	}
}

// A forged/stale country (no catalog match, or not in countryTaxLocale at
// all) must be rejected — no install attempt, clean redirect back to the
// wizard. The request body must never get to pick an arbitrary listing.
func TestSetupTaxPluginInstallRejectsCountryWithNoMatch(t *testing.T) {
	resetTaxCatalogForTest(t)
	mux, dp := newRealDBDeps(t)
	initTestPaths(t)
	mkt := newFakeMarketplace(t, map[string]string{"listing-tax-de": "ut-plugin-tax-de"})
	mkt.setCatalog(deTaxCatalogEntry("listing-tax-de", "ut-plugin-tax-de", "1.0.0"))
	dp.Cfg.Marketplace = mkt.config()

	for _, country := range []string{"US", "FR"} {
		rec := postForm(mux, "/api/setup/tax-plugin", url.Values{"country": {country}}, nil)
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/setup" {
			t.Fatalf("POST with country=%s: code=%d loc=%q, want 303 -> /setup",
				country, rec.Code, rec.Header().Get("Location"))
		}
	}
	if hits := mkt.downloadTokenHits(); hits != 0 {
		t.Fatalf("a rejected country must never trigger an install attempt, got %d download-token hits", hits)
	}
	if pending, _ := loadPendingBasePlugins(t.Context(), dp); len(pending) != 0 {
		t.Fatalf("a rejected country must not be queued for retry, got %+v", pending)
	}
}

// A stale request for a listing that's already installed+active is also a
// clean no-op redirect, never a duplicate install.
func TestSetupTaxPluginInstallRejectsAlreadyInstalled(t *testing.T) {
	resetTaxCatalogForTest(t)
	mux, dp := newRealDBDeps(t)
	initTestPaths(t)
	mkt := newFakeMarketplace(t, map[string]string{"listing-tax-de": "ut-plugin-tax-de"})
	mkt.setCatalog(deTaxCatalogEntry("listing-tax-de", "ut-plugin-tax-de", "1.0.0"))
	dp.Cfg.Marketplace = mkt.config()

	spec := basePluginSpec{CanonicalType: "tax", Locale: "de"}
	if err := resolveAndInstallBasePlugin(t.Context(), dp, spec); err != nil {
		t.Fatalf("seed install: %v", err)
	}
	resetTaxCatalogForTest(t) // clear the TTL cache so the re-check below hits the DB, not a stale cached "installable" answer

	rec := postForm(mux, "/api/setup/tax-plugin", url.Values{"country": {"DE"}}, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/setup?tax_country=DE" {
		t.Fatalf("POST for an already-installed plugin: code=%d loc=%q, want 303 -> /setup?tax_country=DE",
			rec.Code, rec.Header().Get("Location"))
	}
	if hits := mkt.downloadTokenHits(); hits != 1 {
		t.Fatalf("expected exactly the one seed download-token hit, no second install attempt, got %d", hits)
	}
}

// Install failure/timeout: the spec joins the EXISTING ut-docs#591 pending
// list (no second retry mechanism), and the redirect carries
// tax_plugin_pending=1 so the wizard can say so.
func TestSetupTaxPluginInstallFailureJoinsPendingList(t *testing.T) {
	resetTaxCatalogForTest(t)
	mux, dp := newRealDBDeps(t)
	initTestPaths(t)
	mkt := newFakeMarketplace(t, map[string]string{"listing-tax-de": "ut-plugin-tax-de"})
	mkt.setCatalog(deTaxCatalogEntry("listing-tax-de", "ut-plugin-tax-de", "1.0.0"))
	dp.Cfg.Marketplace = mkt.config()

	// Prime the cache while online...
	if plugin, unavailable := setupInstallableTaxPlugin(t.Context(), dp, "DE"); unavailable || plugin == nil {
		t.Fatalf("priming the catalog cache: plugin=%+v unavailable=%v", plugin, unavailable)
	}

	// ...then the network goes away before the operator taps the button. A
	// closed local server fails immediately, so this doesn't wait out
	// setupWizardTaxInstallTimeout's real 20s.
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	dp.Cfg.Marketplace.EndpointURL = deadURL

	rec := postForm(mux, "/api/setup/tax-plugin", url.Values{"country": {"DE"}}, nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST with install failing: code=%d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/setup?tax_country=DE&tax_plugin_pending=1" {
		t.Fatalf("failure redirect = %q, want /setup?tax_country=DE&tax_plugin_pending=1", loc)
	}
	pending, err := loadPendingBasePlugins(t.Context(), dp)
	if err != nil {
		t.Fatalf("loadPendingBasePlugins: %v", err)
	}
	if len(pending) != 1 || pending[0] != (basePluginSpec{CanonicalType: "tax", Locale: "de"}) {
		t.Fatalf("expected the tax/de spec pending for the background retry, got %+v", pending)
	}
	if active, _ := data.NewPluginRepo(dp.Db).PluginActive(t.Context(), "ut-plugin-tax-de"); active {
		t.Fatal("nothing should be installed while the marketplace is unreachable")
	}

	// Network returns: the EXISTING background retry finishes the job.
	dp.Cfg.Marketplace = mkt.config()
	basePluginRetryTick(t.Context(), dp)
	if pending, _ := loadPendingBasePlugins(t.Context(), dp); len(pending) != 0 {
		t.Fatalf("expected the pending list cleared after the retry tick, got %+v", pending)
	}
	if active, _ := data.NewPluginRepo(dp.Db).PluginActive(t.Context(), "ut-plugin-tax-de"); !active {
		t.Fatal("expected the tax plugin installed by the existing retry tick")
	}
}

// Same first-boot-only window as POST /api/setup/language and POST /api/setup.
func TestSetupTaxPluginInstallRefusedAfterFirstBoot(t *testing.T) {
	resetTaxCatalogForTest(t)
	mux, svc, _ := newFullAuthDeps(t)
	id, err := svc.Repo().CreateUser(t.Context(), "boss", "Boss", "admin")
	if err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPIN("1234")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Repo().SetUserPIN(t.Context(), id, hash); err != nil {
		t.Fatal(err)
	}

	rec := postForm(mux, "/api/setup/tax-plugin", url.Values{"country": {"DE"}}, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("POST /api/setup/tax-plugin after first boot: code=%d loc=%q, want 303 -> /login",
			rec.Code, rec.Header().Get("Location"))
	}
}

// --- Wizard render: install tile appears/disappears ---

// The install tile appears for DE with a matching catalog listing, and does
// NOT appear for a country with no mapping.
func TestSetupWizardShowsTaxPluginInstallTileForDEOnly(t *testing.T) {
	resetTaxCatalogForTest(t)
	mux, dp := newRealDBDeps(t)
	initTestPaths(t)
	mkt := newFakeMarketplace(t, map[string]string{"listing-tax-de": "ut-plugin-tax-de"})
	mkt.setCatalog(deTaxCatalogEntry("listing-tax-de", "ut-plugin-tax-de", "1.0.0"))
	dp.Cfg.Marketplace = mkt.config()

	// DE re-render via a PIN-mismatch POST (renderWizard's own re-render
	// path — see TestSetupWizardPINErrorRerenderKeepsOperatorCountryNotDetected
	// for the same pattern), which sets code = the posted country.
	form := url.Values{
		"pin": {"1234"}, "pin_confirm": {"9999"}, // mismatch -> error re-render
		"country": {"DE"}, "currency": {"EUR"},
	}
	rec := postFormRaw(mux, "/api/setup", form)
	body := rec.Body.String()
	if !strings.Contains(body, `action="/api/setup/tax-plugin"`) {
		t.Fatalf("DE re-render missing the tax-plugin install form:\n%s", body)
	}
	if !strings.Contains(body, `name="country" value="DE"`) {
		t.Error("tax-plugin install form is missing its hidden country=DE input")
	}
	// ut-docs#1180 review finding (2026-08-27): the tile's copy previously
	// called this plugin "Optional — install it now, or any time later from
	// the plugin catalog", which is misleading — ADR-0048's hard sales gate
	// means a live German shop cannot complete any real sale without it.
	// Checks the exact old phrase (not a bare "Optional" substring — the
	// shop-type step's own, unrelated "Optional — helps tailor..." hint is
	// legitimately present elsewhere in this same rendered body, since
	// Alpine's x-show keeps every step in the DOM).
	if strings.Contains(body, "Optional — install it now, or any time later from the plugin catalog") {
		t.Error("tax-plugin tile must not call this 'Optional' — it is required before any real sale can complete (ADR-0048)")
	}

	// ut-docs#1460: the tile's presence in the rendered HTML is no longer
	// gated by which country was posted — it's resolved against the fixed
	// Germany country code unconditionally (see setup_page.go), because
	// step 2's country picker never round-trips to the server, so the
	// FIRST render is the only chance this markup ever lands in the DOM.
	// It stays out of an FR operator's way for the reason that mattered
	// all along: the tile only lives inside step 3, which setup.html's
	// `country === 'DE' ? 3 : 4` never routes a non-DE pick into, per
	// ADR-0053 — not because the server withheld the markup.
	form["country"] = []string{"FR"}
	rec = postFormRaw(mux, "/api/setup", form)
	body = rec.Body.String()
	if !strings.Contains(body, `action="/api/setup/tax-plugin"`) {
		t.Error("the DE tax-plugin tile must still be present in the DOM even on an FR re-render — Alpine, not the server, is what keeps it off an FR operator's screen")
	}
	// Review finding (ut-docs#1460): with the server-side gate gone, this
	// ternary is now THE ONLY thing standing between the unconditionally-
	// rendered DE tax tile and a non-DE operator's screen. Nothing
	// previously pinned it, so a later change that made step 3 reachable
	// for any other reason (a step-jump nav, a "confirm business details"
	// step for every country) could ship this tile to a French operator
	// with an otherwise fully green suite. Pin both ternaries that gate it
	// (step 2's Next, step 4's Back) directly.
	if !strings.Contains(body, `step = country === 'DE' ? 3 : 4`) {
		t.Error("step 2's Next must stay Germany-only — it is what keeps the unconditionally-rendered DE tax tile off a non-DE operator's screen (ut-docs#1460)")
	}
	if !strings.Contains(body, `step = country === 'DE' ? 3 : 2`) {
		t.Error("step 4's Back must stay Germany-only for the same reason (ut-docs#1460)")
	}
}

// ut-docs#1460: the tile must be present in the DOM even when the OS-
// detected country isn't Germany. Step 2's country picker is pure Alpine —
// picking DE there never triggers a server round-trip — so whatever
// installableTaxPlugin resolves against on the FIRST render is the only
// chance the tile's markup ever gets into the DOM at all; Alpine's
// `x-show="step === 3"` is what reveals it once the operator later steps
// into step 3, not a fresh server render. Before the fix,
// installableTaxPlugin was resolved from the OS-detected `code`
// (setup_page.go), so a till whose OS locale/timezone wasn't already
// German-tagged (the pilot café's TECLAST tablet: en-GB/Europe/London) got
// no tile in the DOM at all on the initial GET — no later client-side
// country pick could resurrect markup that was never rendered.
func TestSetupWizardTaxPluginTilePresentEvenWhenOSCountryIsNotDE(t *testing.T) {
	resetTaxCatalogForTest(t)
	resetSetupLanguageCatalog()
	t.Cleanup(resetSetupLanguageCatalog)
	withOSLocale(t, "en_GB.UTF-8", "Europe/London")
	mux, _, dp := newFullAuthDeps(t)
	initTestPaths(t)
	mkt := newFakeMarketplace(t, map[string]string{"listing-tax-de": "ut-plugin-tax-de"})
	mkt.setCatalog(deTaxCatalogEntry("listing-tax-de", "ut-plugin-tax-de", "1.0.0"))
	dp.Cfg.Marketplace = mkt.config()

	// en is a core locale, so this is the same "already the default"
	// redirect TestSetupWizardRedirectsToDetectedEnglishAndPrefillsGB pins.
	rec := getSetup(mux, "", "")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/setup?lang=en" {
		t.Fatalf("GET /setup (en_GB, Europe/London): code=%d loc=%q, want 303 -> /setup?lang=en",
			rec.Code, rec.Header().Get("Location"))
	}

	body := getSetup(mux, "?lang=en", "").Body.String()
	if !strings.Contains(body, "country: 'GB'") {
		t.Fatalf("sanity check failed: expected OS-detected country GB, got:\n%s", body)
	}
	if !strings.Contains(body, `action="/api/setup/tax-plugin"`) {
		t.Errorf("expected the DE tax-plugin install tile present in the DOM even though the OS-detected country is GB (Alpine reveals it once the operator picks DE), got:\n%s", body)
	}
}

// After a real install, the tile is gone on the next render.
func TestSetupWizardTaxPluginTileGoneAfterInstall(t *testing.T) {
	resetTaxCatalogForTest(t)
	mux, dp := newRealDBDeps(t)
	initTestPaths(t)
	mkt := newFakeMarketplace(t, map[string]string{"listing-tax-de": "ut-plugin-tax-de"})
	mkt.setCatalog(deTaxCatalogEntry("listing-tax-de", "ut-plugin-tax-de", "1.0.0"))
	dp.Cfg.Marketplace = mkt.config()

	rec := postForm(mux, "/api/setup/tax-plugin", url.Values{"country": {"DE"}}, nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("install: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if active, _ := data.NewPluginRepo(dp.Db).PluginActive(t.Context(), "ut-plugin-tax-de"); !active {
		t.Fatal("expected the tax plugin installed")
	}

	form := url.Values{
		"pin": {"1234"}, "pin_confirm": {"9999"},
		"country": {"DE"}, "currency": {"EUR"},
	}
	rec = postFormRaw(mux, "/api/setup", form)
	if strings.Contains(rec.Body.String(), `action="/api/setup/tax-plugin"`) {
		t.Error("the tax-plugin install tile must be gone once the plugin is actually installed")
	}
}

// --- Review follow-up: the install round-trip resumes step 3 ---

// The tax tile lives on step 3, not step 1 like the language tiles, so the
// install redirect has to bring the operator back to step 3 with the country
// they picked — a bare /setup would open the wizard at step 1 and re-derive
// the country from OS detection, silently discarding a hand-picked DE (and
// anything typed on step 3).
func TestSetupGETResumesStep3ForTaxCountry(t *testing.T) {
	resetTaxCatalogForTest(t)
	resetSetupLanguageCatalog()
	t.Cleanup(resetSetupLanguageCatalog)
	// A bare GET with no explicit ?lang= and no ut_lang cookie is what a real
	// first-ever visit looks like, so it's also what triggers renderWizard's
	// own OS-language-auto-detect redirect (setup_page.go) when the runtime's
	// $LANG/$LC_ALL happens to resolve to a shipped locale — invisible in any
	// sandbox where that env is unset, which is exactly how this slipped past
	// local runs and review and only broke in CI (LANG=en_US.UTF-8 there).
	// withOSLocale(t, "", "") is this file's own established way (see
	// setup_page_test.go's other GET /setup tests) to make that redirect
	// deterministically NOT fire, so this test verifies the resume behaviour
	// itself rather than racing the runner's locale.
	withOSLocale(t, "", "")
	mux, dp := newRealDBDeps(t)
	initTestPaths(t)
	mkt := newFakeMarketplace(t, map[string]string{"listing-tax-de": "ut-plugin-tax-de"})
	mkt.setCatalog(deTaxCatalogEntry("listing-tax-de", "ut-plugin-tax-de", "1.0.0"))
	dp.Cfg.Marketplace = mkt.config()

	body := getSetup(mux, "?tax_country=DE", "").Body.String()
	if !strings.Contains(body, "step: 3,") {
		t.Errorf("GET /setup?tax_country=DE must open the wizard on step 3, got:\n%s", body)
	}
	// ...and with the country restored, so step 3 is reachable at all and the
	// hidden currency/tax inputs bound to it aren't reset.
	if !strings.Contains(body, "country: 'DE'") {
		t.Error("GET /setup?tax_country=DE must restore country=DE in the wizard's x-data")
	}
	// The tile is still there to install (nothing installed yet).
	if !strings.Contains(body, `action="/api/setup/tax-plugin"`) {
		t.Error("expected the tax-plugin install tile on the resumed step 3")
	}
}

// A bare GET (no tax_country) still opens on step 1, and a tax_country the
// wizard has no business honouring — not tax-mapped, or not a real wizard
// country — is ignored rather than steering the wizard.
func TestSetupGETTaxCountryResumeIsNotForgeable(t *testing.T) {
	resetTaxCatalogForTest(t)
	resetSetupLanguageCatalog()
	t.Cleanup(resetSetupLanguageCatalog)
	withOSLocale(t, "", "") // see TestSetupGETResumesStep3ForTaxCountry's comment
	mux, dp := newRealDBDeps(t)
	initTestPaths(t)
	mkt := newFakeMarketplace(t, map[string]string{"listing-tax-de": "ut-plugin-tax-de"})
	mkt.setCatalog(deTaxCatalogEntry("listing-tax-de", "ut-plugin-tax-de", "1.0.0"))
	dp.Cfg.Marketplace = mkt.config()

	for _, q := range []string{"", "?tax_country=", "?tax_country=FR", "?tax_country=ZZ", "?tax_country=OTHER"} {
		body := getSetup(mux, q, "").Body.String()
		if !strings.Contains(body, "step: 1,") {
			t.Errorf("GET /setup%s must open on step 1, got:\n%s", q, body)
		}
	}
}

// The failure path resumes step 3 too, and shows the "still installing"
// note there — the note is inside the tile, so landing on step 1 would have
// hidden it entirely.
func TestSetupGETResumeShowsTaxPluginPendingNote(t *testing.T) {
	resetTaxCatalogForTest(t)
	resetSetupLanguageCatalog()
	t.Cleanup(resetSetupLanguageCatalog)
	withOSLocale(t, "", "") // see TestSetupGETResumesStep3ForTaxCountry's comment
	mux, dp := newRealDBDeps(t)
	initTestPaths(t)
	mkt := newFakeMarketplace(t, map[string]string{"listing-tax-de": "ut-plugin-tax-de"})
	mkt.setCatalog(deTaxCatalogEntry("listing-tax-de", "ut-plugin-tax-de", "1.0.0"))
	dp.Cfg.Marketplace = mkt.config()

	body := getSetup(mux, "?tax_country=DE&tax_plugin_pending=1", "").Body.String()
	if !strings.Contains(body, "step: 3,") {
		t.Error("the failure redirect's target must also resume on step 3")
	}
	if !strings.Contains(body, "data-tax-plugin-pending") {
		t.Errorf("expected the background-install note on the resumed page, got:\n%s", body)
	}
}

// --- setupTaxPluginSkipHandler (POST /api/setup/tax-plugin-skip) ---
//
// ut-docs#1506: before this handler existed, step 3's "Skip for now" only
// ever cleared the TSE business-identity fields and advanced to step 4 —
// nothing joined the ut-docs#591 pending/retry list, so the tile's own
// "we'll keep retrying in the background" promise was false for anyone who
// left through this door instead of a failed Install click. These tests
// mirror TestSetupTaxPluginInstallFailureJoinsPendingList and its siblings
// above, one door over.

// A skip while a real, not-yet-installed match exists queues the SAME
// tax/de spec the install handler's failure branch would, and the
// background retry (ut-docs#591) actually finishes the job later — the
// literal promise this card exists to make true.
func TestSetupTaxPluginSkipQueuesPendingAndBackgroundRetryInstallsIt(t *testing.T) {
	resetTaxCatalogForTest(t)
	mux, dp := newRealDBDeps(t)
	initTestPaths(t)
	mkt := newFakeMarketplace(t, map[string]string{"listing-tax-de": "ut-plugin-tax-de"})
	mkt.setCatalog(deTaxCatalogEntry("listing-tax-de", "ut-plugin-tax-de", "1.0.0"))
	dp.Cfg.Marketplace = mkt.config()

	rec := postForm(mux, "/api/setup/tax-plugin-skip", url.Values{"country": {"DE"}}, nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /api/setup/tax-plugin-skip: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/setup?tax_country=DE&tax_plugin_skip_ack=1" {
		t.Fatalf("skip redirect = %q, want /setup?tax_country=DE&tax_plugin_skip_ack=1", loc)
	}

	pending, err := loadPendingBasePlugins(t.Context(), dp)
	if err != nil {
		t.Fatalf("loadPendingBasePlugins: %v", err)
	}
	if len(pending) != 1 || pending[0] != (basePluginSpec{CanonicalType: "tax", Locale: "de"}) {
		t.Fatalf("expected the tax/de spec queued for background retry after a skip, got %+v", pending)
	}
	if active, _ := data.NewPluginRepo(dp.Db).PluginActive(t.Context(), "ut-plugin-tax-de"); active {
		t.Fatal("a skip must never install anything itself")
	}

	// The EXISTING background retry (ut-docs#591) finishes the job later —
	// no second retry mechanism for the skip path.
	basePluginRetryTick(t.Context(), dp)
	if pending, _ := loadPendingBasePlugins(t.Context(), dp); len(pending) != 0 {
		t.Fatalf("expected the pending list cleared after the retry tick, got %+v", pending)
	}
	if active, _ := data.NewPluginRepo(dp.Db).PluginActive(t.Context(), "ut-plugin-tax-de"); !active {
		t.Fatal("expected the background retry to install the plugin the skip left pending")
	}
}

// A forged/stale country (no catalog match, or not tax-mapped at all) is
// rejected clean — nothing queued, same posture as the install handler's
// own reject test.
func TestSetupTaxPluginSkipRejectsCountryWithNoMatch(t *testing.T) {
	resetTaxCatalogForTest(t)
	mux, dp := newRealDBDeps(t)
	initTestPaths(t)
	mkt := newFakeMarketplace(t, map[string]string{"listing-tax-de": "ut-plugin-tax-de"})
	mkt.setCatalog(deTaxCatalogEntry("listing-tax-de", "ut-plugin-tax-de", "1.0.0"))
	dp.Cfg.Marketplace = mkt.config()

	for _, country := range []string{"US", "FR"} {
		rec := postForm(mux, "/api/setup/tax-plugin-skip", url.Values{"country": {country}}, nil)
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/setup" {
			t.Fatalf("POST with country=%s: code=%d loc=%q, want 303 -> /setup",
				country, rec.Code, rec.Header().Get("Location"))
		}
	}
	if pending, _ := loadPendingBasePlugins(t.Context(), dp); len(pending) != 0 {
		t.Fatalf("a rejected country must not be queued, got %+v", pending)
	}
}

// A stale skip for a listing that's already installed+active queues
// nothing — there's genuinely nothing left to retry.
func TestSetupTaxPluginSkipNoOpWhenAlreadyInstalled(t *testing.T) {
	resetTaxCatalogForTest(t)
	mux, dp := newRealDBDeps(t)
	initTestPaths(t)
	mkt := newFakeMarketplace(t, map[string]string{"listing-tax-de": "ut-plugin-tax-de"})
	mkt.setCatalog(deTaxCatalogEntry("listing-tax-de", "ut-plugin-tax-de", "1.0.0"))
	dp.Cfg.Marketplace = mkt.config()

	spec := basePluginSpec{CanonicalType: "tax", Locale: "de"}
	if err := resolveAndInstallBasePlugin(t.Context(), dp, spec); err != nil {
		t.Fatalf("seed install: %v", err)
	}
	resetTaxCatalogForTest(t) // clear the TTL cache so the re-check hits the DB, not a stale cached "installable" answer

	rec := postForm(mux, "/api/setup/tax-plugin-skip", url.Values{"country": {"DE"}}, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/setup?tax_country=DE&tax_plugin_skip_ack=1" {
		t.Fatalf("skip for an already-installed plugin: code=%d loc=%q, want 303 -> /setup?tax_country=DE&tax_plugin_skip_ack=1",
			rec.Code, rec.Header().Get("Location"))
	}
	if pending, _ := loadPendingBasePlugins(t.Context(), dp); len(pending) != 0 {
		t.Fatalf("nothing should be queued once the plugin is already active, got %+v", pending)
	}
}

// Same first-boot-only window as its install sibling.
func TestSetupTaxPluginSkipRefusedAfterFirstBoot(t *testing.T) {
	resetTaxCatalogForTest(t)
	mux, svc, _ := newFullAuthDeps(t)
	id, err := svc.Repo().CreateUser(t.Context(), "boss", "Boss", "admin")
	if err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPIN("1234")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Repo().SetUserPIN(t.Context(), id, hash); err != nil {
		t.Fatal(err)
	}

	rec := postForm(mux, "/api/setup/tax-plugin-skip", url.Values{"country": {"DE"}}, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("POST /api/setup/tax-plugin-skip after first boot: code=%d loc=%q, want 303 -> /login",
			rec.Code, rec.Header().Get("Location"))
	}
}

// --- Wizard render: the skip round-trip resumes step 4 and warns plainly ---

// Unlike the install failure round-trip (resumes step 3, softer
// install_pending note), an explicit skip resumes step 4 — where "Skip for
// now" was always headed — but with the stronger skip_warning note visible
// on landing, not buried back on step 3 behind another click.
func TestSetupGETResumeStep4AfterTaxPluginSkipAndShowsWarning(t *testing.T) {
	resetTaxCatalogForTest(t)
	resetSetupLanguageCatalog()
	t.Cleanup(resetSetupLanguageCatalog)
	withOSLocale(t, "", "") // see TestSetupGETResumesStep3ForTaxCountry's comment
	mux, dp := newRealDBDeps(t)
	initTestPaths(t)
	mkt := newFakeMarketplace(t, map[string]string{"listing-tax-de": "ut-plugin-tax-de"})
	mkt.setCatalog(deTaxCatalogEntry("listing-tax-de", "ut-plugin-tax-de", "1.0.0"))
	dp.Cfg.Marketplace = mkt.config()

	body := getSetup(mux, "?tax_country=DE&tax_plugin_skip_ack=1", "").Body.String()
	if !strings.Contains(body, "step: 4,") {
		t.Errorf("GET /setup?tax_country=DE&tax_plugin_skip_ack=1 must open the wizard on step 4, got:\n%s", body)
	}
	if !strings.Contains(body, "data-tax-plugin-skipped") {
		t.Errorf("expected the skip_warning note on the resumed page, got:\n%s", body)
	}
}

// tax_plugin_skip_ack alone (no tax_country) must not fake the warning or
// steer the wizard — same "not forgeable on its own" posture as
// TestSetupGETTaxCountryResumeIsNotForgeable.
func TestSetupGETTaxPluginSkipAckWithoutTaxCountryIsIgnored(t *testing.T) {
	resetTaxCatalogForTest(t)
	resetSetupLanguageCatalog()
	t.Cleanup(resetSetupLanguageCatalog)
	withOSLocale(t, "", "")
	mux, dp := newRealDBDeps(t)
	initTestPaths(t)
	mkt := newFakeMarketplace(t, map[string]string{"listing-tax-de": "ut-plugin-tax-de"})
	mkt.setCatalog(deTaxCatalogEntry("listing-tax-de", "ut-plugin-tax-de", "1.0.0"))
	dp.Cfg.Marketplace = mkt.config()

	body := getSetup(mux, "?tax_plugin_skip_ack=1", "").Body.String()
	if !strings.Contains(body, "step: 1,") {
		t.Errorf("GET /setup?tax_plugin_skip_ack=1 (no tax_country) must open on step 1, got:\n%s", body)
	}
	if strings.Contains(body, "data-tax-plugin-skipped") {
		t.Error("tax_plugin_skip_ack alone, without tax_country, must not show the skip warning")
	}
}

// The step 3 render must target the skip at the new tax-plugin-skip form
// (not just leave it a client-only step change) whenever the tile is
// actually showing — this is what makes the queue-on-skip fix reachable
// from the real UI, not just from a direct POST in a test.
func TestSetupWizardSkipButtonTargetsSkipFormWhenTileShowing(t *testing.T) {
	resetTaxCatalogForTest(t)
	mux, dp := newRealDBDeps(t)
	initTestPaths(t)
	mkt := newFakeMarketplace(t, map[string]string{"listing-tax-de": "ut-plugin-tax-de"})
	mkt.setCatalog(deTaxCatalogEntry("listing-tax-de", "ut-plugin-tax-de", "1.0.0"))
	dp.Cfg.Marketplace = mkt.config()

	form := url.Values{
		"pin": {"1234"}, "pin_confirm": {"9999"}, // mismatch -> error re-render, tile present
		"country": {"DE"}, "currency": {"EUR"},
	}
	body := postFormRaw(mux, "/api/setup", form).Body.String()
	if !strings.Contains(body, `action="/api/setup/tax-plugin-skip"`) {
		t.Errorf("expected the tax-plugin-skip form present while the tile is showing, got:\n%s", body)
	}
	if !strings.Contains(body, `form="tax-plugin-skip"`) {
		t.Errorf("expected the Skip button wired to form=tax-plugin-skip while the tile is showing, got:\n%s", body)
	}
}

// ut-docs#1506 review finding B1: Skip was not the only door off step 3 that
// left the tile's plugin uninstalled with nothing queued — Next (the
// PRIMARY button) did too, and an operator who filled in the TSE fields (the
// step's actual content) and pressed Next is arguably the MORE likely path
// than Skip. Next must stay a pure client-side step change (a full
// round-trip would lose whatever was just typed into the TSE fields, since
// nothing repopulates them on a bare GET resume) — so it fires the same
// queue request in the background via htmx instead of navigating.
func TestSetupWizardNextButtonQueuesTaxPluginInBackgroundWhenTileShowing(t *testing.T) {
	resetTaxCatalogForTest(t)
	mux, dp := newRealDBDeps(t)
	initTestPaths(t)
	mkt := newFakeMarketplace(t, map[string]string{"listing-tax-de": "ut-plugin-tax-de"})
	mkt.setCatalog(deTaxCatalogEntry("listing-tax-de", "ut-plugin-tax-de", "1.0.0"))
	dp.Cfg.Marketplace = mkt.config()

	form := url.Values{
		"pin": {"1234"}, "pin_confirm": {"9999"}, // mismatch -> error re-render, tile present
		"country": {"DE"}, "currency": {"EUR"},
	}
	body := postFormRaw(mux, "/api/setup", form).Body.String()
	if !strings.Contains(body, `hx-post="/api/setup/tax-plugin-skip" hx-include="#tax-plugin-skip" hx-swap="none"`) {
		t.Errorf("expected the Next button wired to fire the background queue request while the tile is showing, got:\n%s", body)
	}
}

// The opposite of the test above: once the plugin is already installed (no
// tile), Next must NOT carry the background-queue wiring — there's nothing
// left to queue, and the hx-post attributes would 404 the include target
// (the tax-plugin-skip form only renders alongside the tile).
func TestSetupWizardNextButtonHasNoBackgroundQueueWhenTileAbsent(t *testing.T) {
	resetTaxCatalogForTest(t)
	mux, dp := newRealDBDeps(t)
	initTestPaths(t)
	mkt := newFakeMarketplace(t, map[string]string{"listing-tax-de": "ut-plugin-tax-de"})
	mkt.setCatalog(deTaxCatalogEntry("listing-tax-de", "ut-plugin-tax-de", "1.0.0"))
	dp.Cfg.Marketplace = mkt.config()

	rec := postForm(mux, "/api/setup/tax-plugin", url.Values{"country": {"DE"}}, nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("install: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if active, _ := data.NewPluginRepo(dp.Db).PluginActive(t.Context(), "ut-plugin-tax-de"); !active {
		t.Fatal("expected the tax plugin installed")
	}

	form := url.Values{
		"pin": {"1234"}, "pin_confirm": {"9999"},
		"country": {"DE"}, "currency": {"EUR"},
	}
	body := postFormRaw(mux, "/api/setup", form).Body.String()
	if strings.Contains(body, `hx-post="/api/setup/tax-plugin-skip"`) {
		t.Error("Next must not carry the background-queue wiring once the plugin is already installed (no tile, no include target)")
	}
}

// postFormRaw posts to POST /api/setup directly (unlike postForm, no
// pre-auth user context is needed here — the wizard's own handler is
// auth-exempt during first boot).
func postFormRaw(mux *http.ServeMux, path string, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// --- ut-docs#1512: fully offline first boot ---

// Pressing the offline tile's button is the operator's consent: it queues the
// fiscal plugin on the existing #591 pending list straight away (no 20s
// foreground attempt against a catalog already known unreachable) and lands
// back on step 3 with the "still installing in the background" note. The
// existing background retry installs it once the network returns.
func TestSetupTaxPluginInstallOfflineQueuesPendingWithConsent(t *testing.T) {
	resetTaxCatalogForTest(t)
	mux, dp := newRealDBDeps(t)
	initTestPaths(t)
	deadMarketplace(t, dp)

	rec := postForm(mux, "/api/setup/tax-plugin", url.Values{"country": {"DE"}}, nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("offline POST /api/setup/tax-plugin: code=%d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/setup?tax_country=DE&tax_plugin_pending=1" {
		t.Fatalf("offline install redirect = %q, want /setup?tax_country=DE&tax_plugin_pending=1", loc)
	}
	pending, err := loadPendingBasePlugins(t.Context(), dp)
	if err != nil {
		t.Fatalf("loadPendingBasePlugins: %v", err)
	}
	if len(pending) != 1 || pending[0] != (basePluginSpec{CanonicalType: "tax", Locale: "de"}) {
		t.Fatalf("expected the tax/de spec queued after offline consent, got %+v", pending)
	}

	// Network returns: the EXISTING background retry finishes the job.
	mkt := newFakeMarketplace(t, map[string]string{"listing-tax-de": "ut-plugin-tax-de"})
	mkt.setCatalog(deTaxCatalogEntry("listing-tax-de", "ut-plugin-tax-de", "1.0.0"))
	dp.Cfg.Marketplace = mkt.config()
	basePluginRetryTick(t.Context(), dp)
	if active, _ := data.NewPluginRepo(dp.Db).PluginActive(t.Context(), "ut-plugin-tax-de"); !active {
		t.Fatal("expected the background retry to install the plugin queued while offline")
	}
}

// Skip (and Next, which fires the same request) off the offline tile queue it
// too, exactly like #1506's online tile — the tile was shown, so the operator
// was prompted.
func TestSetupTaxPluginSkipOfflineQueuesPending(t *testing.T) {
	resetTaxCatalogForTest(t)
	mux, dp := newRealDBDeps(t)
	initTestPaths(t)
	deadMarketplace(t, dp)

	rec := postForm(mux, "/api/setup/tax-plugin-skip", url.Values{"country": {"DE"}}, nil)
	if loc := rec.Header().Get("Location"); loc != "/setup?tax_country=DE&tax_plugin_skip_ack=1" {
		t.Fatalf("offline skip redirect = %q, want /setup?tax_country=DE&tax_plugin_skip_ack=1", loc)
	}
	pending, _ := loadPendingBasePlugins(t.Context(), dp)
	if len(pending) != 1 || pending[0] != (basePluginSpec{CanonicalType: "tax", Locale: "de"}) {
		t.Fatalf("expected the tax/de spec queued after an offline skip, got %+v", pending)
	}
}

// The wizard renders the tile offline, in its offline state: the "couldn't
// check yet" note, the "Install when online" button, and the same Skip/Next
// wiring as the online tile.
func TestSetupWizardShowsOfflineTaxPluginTile(t *testing.T) {
	resetTaxCatalogForTest(t)
	mux, dp := newRealDBDeps(t)
	initTestPaths(t)
	deadMarketplace(t, dp)

	form := url.Values{
		"pin": {"1234"}, "pin_confirm": {"9999"}, // mismatch -> error re-render
		"country": {"DE"}, "currency": {"EUR"},
	}
	body := postFormRaw(mux, "/api/setup", form).Body.String()
	for _, want := range []string{
		`action="/api/setup/tax-plugin"`,
		`action="/api/setup/tax-plugin-skip"`,
		`form="tax-plugin-skip"`,
		`hx-post="/api/setup/tax-plugin-skip" hx-include="#tax-plugin-skip" hx-swap="none"`,
		`data-tax-plugin-offline`,
		"Install when online",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("offline DE render missing %q", want)
		}
	}
}

// The online tile must not carry the offline note.
func TestSetupWizardOnlineTaxPluginTileHasNoOfflineNote(t *testing.T) {
	resetTaxCatalogForTest(t)
	mux, dp := newRealDBDeps(t)
	initTestPaths(t)
	mkt := newFakeMarketplace(t, map[string]string{"listing-tax-de": "ut-plugin-tax-de"})
	mkt.setCatalog(deTaxCatalogEntry("listing-tax-de", "ut-plugin-tax-de", "1.0.0"))
	dp.Cfg.Marketplace = mkt.config()

	form := url.Values{"pin": {"1234"}, "pin_confirm": {"9999"}, "country": {"DE"}, "currency": {"EUR"}}
	body := postFormRaw(mux, "/api/setup", form).Body.String()
	if strings.Contains(body, "data-tax-plugin-offline") || strings.Contains(body, "Install when online") {
		t.Error("the online tile must not show the offline note or button")
	}
}

// After consenting offline, the resumed step 3 confirms the install is queued
// (not "still installing", which would be untrue with no network).
func TestSetupGETResumeAfterOfflineConsentShowsQueuedNote(t *testing.T) {
	resetTaxCatalogForTest(t)
	withOSLocale(t, "", "") // see TestSetupGETResumesStep3ForTaxCountry's comment
	mux, dp := newRealDBDeps(t)
	initTestPaths(t)
	deadMarketplace(t, dp)

	body := getSetup(mux, "?tax_country=DE&tax_plugin_pending=1", "").Body.String()
	if !strings.Contains(body, "data-tax-plugin-queued") {
		t.Errorf("offline resume after consent must show the queued note, got:\n%s", body)
	}
	if strings.Contains(body, "data-tax-plugin-offline") {
		t.Error("the queued note replaces the offline note, not both")
	}
}

// ut-docs#3210: the operator consented to the fiscal plugin (offline tile,
// Skip or Next), so tax/de is on the #591 pending list. If the background
// retry then reaches a catalog that has no DE tax listing (staging endpoint,
// listing unpublished), "nothing published" used to count as done and the
// tick dropped the entry — the pending list, the Settings chip and the tile
// all vanished, leaving a German till with no fiscal plugin and no warning.
// A tax spec must stay pending; a language pack keeps today's silent no-op.
func TestBasePluginRetryTick_TaxSpecStaysPendingWhenCatalogHasNoTaxListing(t *testing.T) {
	resetTaxCatalogForTest(t)
	_, dp := newRealDBDeps(t)
	initTestPaths(t)
	mkt := newFakeMarketplace(t, map[string]string{})
	mkt.setCatalog() // reachable, but publishes no tax (or language) listing
	dp.Cfg.Marketplace = mkt.config()

	taxSpec := basePluginSpec{CanonicalType: "tax", Locale: "de"}
	langSpec := basePluginSpec{CanonicalType: "language", Locale: "de"}
	if err := savePendingBasePlugins(t.Context(), dp, []basePluginSpec{taxSpec, langSpec}); err != nil {
		t.Fatal(err)
	}

	basePluginRetryTick(t.Context(), dp)

	pending, err := loadPendingBasePlugins(t.Context(), dp)
	if err != nil {
		t.Fatalf("loadPendingBasePlugins: %v", err)
	}
	if len(pending) != 1 || pending[0] != taxSpec {
		t.Fatalf("pending after the tick = %+v, want only %+v (tax stays, language no-op drops)", pending, taxSpec)
	}
	// The Settings chip is {{ range .pendingBasePlugins }} over exactly this list.
	if views := pendingBasePluginViews(pending, loadBasePluginsNotPublished(t.Context(), dp)); len(views) != 1 || views[0].CanonicalType != "tax" || views[0].LocaleUpper != "DE" || !views[0].NotPublished {
		t.Fatalf("Settings chip views = %+v, want one not-published tax/DE entry (ut-docs#3243)", views)
	}

	// The listing is published later: the same retry installs it and clears
	// the entry — the stay-pending path converges, it doesn't wedge.
	mkt2 := newFakeMarketplace(t, map[string]string{"listing-tax-de": "ut-plugin-tax-de"})
	mkt2.setCatalog(deTaxCatalogEntry("listing-tax-de", "ut-plugin-tax-de", "1.0.0"))
	dp.Cfg.Marketplace = mkt2.config()
	basePluginRetryTick(t.Context(), dp)
	if pending, _ := loadPendingBasePlugins(t.Context(), dp); len(pending) != 0 {
		t.Fatalf("expected the pending list cleared once the listing is published, got %+v", pending)
	}
	if active, _ := data.NewPluginRepo(dp.Db).PluginActive(t.Context(), "ut-plugin-tax-de"); !active {
		t.Fatal("expected the retry to install the fiscal plugin once it is published")
	}
	if got := loadBasePluginsNotPublished(t.Context(), dp); len(got) != 0 {
		t.Fatalf("the install must clear the not-published reason, got %+v", got)
	}
}

// --- ut-docs#3211: offline, but a tax plugin is already here ---

// seedLocalTaxPlugin installs an active plugin with one `tax` entry straight
// into the real schema — what a sideloaded fiscal plugin, a restored DB or a
// wizard re-run after a reset leaves behind — with the given ADR-0129
// markets (none = every market).
func seedLocalTaxPlugin(t *testing.T, dp *common.Deps, pluginID string, markets ...string) {
	t.Helper()
	ctx := t.Context()
	if _, err := dp.Db.ExecContext(ctx, `
INSERT INTO plugin_catalog (id, version, name, description, runtime, entrypoint, package_url, sha256, author, website, tags_json, min_pos_version, api_version, published_at)
VALUES (?, '1.0.0', 'Local fiscal plugin', 'desc', 'wasm', 'entry', 'url', 'sha', 'auth', 'site', '[]', '0.0.0', '1', datetime('now'))`, pluginID); err != nil {
		t.Fatalf("seed plugin_catalog: %v", err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO plugins (id, name, version, entrypoint, is_active) VALUES (?, 'Local fiscal plugin', '1.0.0', 'entry', 1)`, pluginID); err != nil {
		t.Fatalf("seed plugins: %v", err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO plugin_entries (id, plugin_id, type, key, label) VALUES (?, ?, 'tax', 'tax', 'Tax')`, pluginID+":tax", pluginID); err != nil {
		t.Fatalf("seed plugin_entries: %v", err)
	}
	for _, m := range markets {
		if _, err := dp.Db.ExecContext(ctx, `INSERT INTO plugin_markets (plugin_id, market) VALUES (?, ?)`, pluginID, m); err != nil {
			t.Fatalf("seed plugin_markets: %v", err)
		}
	}
}

// With the catalog unreachable there is no listing id to check, so the
// install-status lookup can't run — but an active local tax plugin for the
// country still means there is nothing to prompt.
func TestSetupInstallableTaxPlugin_CatalogUnreachableWithActiveTaxPluginReturnsNil(t *testing.T) {
	resetTaxCatalogForTest(t)
	dp := newBasePluginTestDeps(t)
	deadMarketplace(t, dp)
	seedLocalTaxPlugin(t, dp, "com.example.sideloaded-tax")

	plugin, unavailable := setupInstallableTaxPlugin(t.Context(), dp, "DE")
	if plugin != nil {
		t.Fatalf("expected no tile with an active tax plugin already installed, got %+v", plugin)
	}
	if unavailable {
		t.Fatal("an already-installed plugin is not a catalog-unavailable case")
	}
}

// A tax plugin whose markets name another country is not this country's:
// the offline tile still shows.
func TestSetupInstallableTaxPlugin_CatalogUnreachableOtherMarketTaxPluginKeepsTile(t *testing.T) {
	resetTaxCatalogForTest(t)
	dp := newBasePluginTestDeps(t)
	deadMarketplace(t, dp)
	seedLocalTaxPlugin(t, dp, "com.example.fr-tax", "FR")

	plugin, _ := setupInstallableTaxPlugin(t.Context(), dp, "DE")
	if plugin == nil || !plugin.Offline {
		t.Fatalf("expected the offline DE tile when the only tax plugin is for FR, got %+v", plugin)
	}
}

// The card's AC end to end: offline, with an active tax plugin, the wizard
// renders no tile, and neither the tile's consent POST nor Skip/Next queues
// a spurious install (which would also light the Settings pending chip).
func TestSetupWizardOfflineWithActiveTaxPluginShowsNoTileAndQueuesNothing(t *testing.T) {
	resetTaxCatalogForTest(t)
	mux, dp := newRealDBDeps(t)
	initTestPaths(t)
	deadMarketplace(t, dp)
	seedLocalTaxPlugin(t, dp, "com.example.sideloaded-tax")

	form := url.Values{
		"pin": {"1234"}, "pin_confirm": {"9999"}, // mismatch -> error re-render
		"country": {"DE"}, "currency": {"EUR"},
	}
	body := postFormRaw(mux, "/api/setup", form).Body.String()
	for _, unwanted := range []string{`action="/api/setup/tax-plugin"`, `data-tax-plugin-offline`} {
		if strings.Contains(body, unwanted) {
			t.Errorf("offline DE render with a tax plugin installed still contains %q", unwanted)
		}
	}

	if loc := postForm(mux, "/api/setup/tax-plugin", url.Values{"country": {"DE"}}, nil).Header().Get("Location"); loc != "/setup?tax_country=DE" {
		t.Fatalf("consent POST redirect = %q, want /setup?tax_country=DE (nothing to install)", loc)
	}
	postForm(mux, "/api/setup/tax-plugin-skip", url.Values{"country": {"DE"}}, nil)
	pending, err := loadPendingBasePlugins(t.Context(), dp)
	if err != nil {
		t.Fatalf("loadPendingBasePlugins: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected nothing queued with a tax plugin already installed, got %+v", pending)
	}
}

// Online too: the install-status check only knows marketplace installs (by
// listing id), so a sideloaded tax plugin used to leave the tile up even with
// the catalog reachable — and its consent attempted a second fiscal install.
func TestSetupInstallableTaxPlugin_OnlineWithSideloadedTaxPluginReturnsNil(t *testing.T) {
	resetTaxCatalogForTest(t)
	dp := newBasePluginTestDeps(t)
	mkt := newFakeMarketplace(t, map[string]string{"listing-tax-de": "ut-plugin-tax-de"})
	mkt.setCatalog(deTaxCatalogEntry("listing-tax-de", "ut-plugin-tax-de", "1.0.0"))
	dp.Cfg.Marketplace = mkt.config()
	seedLocalTaxPlugin(t, dp, "com.example.sideloaded-tax")

	if plugin, unavailable := setupInstallableTaxPlugin(t.Context(), dp, "DE"); plugin != nil || unavailable {
		t.Fatalf("expected no tile online with a sideloaded tax plugin, got plugin=%+v unavailable=%v", plugin, unavailable)
	}
}

// A failed local check fails open: the operator is still prompted.
func TestSetupInstallableTaxPlugin_LocalCheckErrorStillPrompts(t *testing.T) {
	resetTaxCatalogForTest(t)
	dp := newBasePluginTestDeps(t)
	deadMarketplace(t, dp)
	seedLocalTaxPlugin(t, dp, "com.example.sideloaded-tax")
	if _, err := dp.Db.ExecContext(t.Context(), `DROP TABLE plugin_markets`); err != nil {
		t.Fatalf("drop plugin_markets: %v", err)
	}

	plugin, _ := setupInstallableTaxPlugin(t.Context(), dp, "DE")
	if plugin == nil || !plugin.Offline {
		t.Fatalf("expected the offline tile when the local check errors, got %+v", plugin)
	}
}

// --- ut-docs#3511: a pending tax spec is satisfied by an active local plugin ---

// A tax spec queued before a sideload / DB restore stays on the #591 pending
// list. The retry must recognise the local tax plugin for the spec's locale
// and drop the spec WITHOUT browsing the catalog or installing a second
// fiscal plugin (ADR-0129 exclusivity).
func TestBasePluginRetryTick_TaxSpecDroppedWhenLocalTaxPluginActive(t *testing.T) {
	for _, tc := range []struct {
		name    string
		markets []string
	}{
		{"no markets means every market", nil},
		{"markets list the spec's country", []string{"DE"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetTaxCatalogForTest(t)
			_, dp := newRealDBDeps(t)
			initTestPaths(t)
			// Reachable and it DOES publish a DE tax listing: only the
			// local-satisfaction check can keep it from being installed.
			mkt := newFakeMarketplace(t, map[string]string{"listing-tax-de": "ut-plugin-tax-de"})
			mkt.setCatalog(deTaxCatalogEntry("listing-tax-de", "ut-plugin-tax-de", "1.0.0"))
			dp.Cfg.Marketplace = mkt.config()
			seedLocalTaxPlugin(t, dp, "com.example.sideloaded-tax", tc.markets...)

			taxSpec := basePluginSpec{CanonicalType: "tax", Locale: "de"}
			if err := savePendingBasePlugins(t.Context(), dp, []basePluginSpec{taxSpec}); err != nil {
				t.Fatal(err)
			}

			basePluginRetryTick(t.Context(), dp)

			pending, err := loadPendingBasePlugins(t.Context(), dp)
			if err != nil {
				t.Fatalf("loadPendingBasePlugins: %v", err)
			}
			if len(pending) != 0 {
				t.Fatalf("pending after the tick = %+v, want the satisfied tax spec dropped", pending)
			}
			if active, _ := data.NewPluginRepo(dp.Db).PluginActive(t.Context(), "ut-plugin-tax-de"); active {
				t.Fatal("a second fiscal plugin must not be installed over the local one")
			}
			// resolveAndInstallBasePlugin browses with no capability filter.
			if n := mkt.catalogHitsFor(""); n != 0 {
				t.Fatalf("catalog was browsed %d time(s); the local check must come first", n)
			}
			if n := mkt.downloadTokenHits(); n != 0 {
				t.Fatalf("download token requested %d time(s); nothing may be installed", n)
			}
		})
	}
}

// A local tax plugin meant only for another country does not satisfy a DE
// spec: it stays pending (here the catalog is unreachable).
func TestBasePluginRetryTick_TaxSpecStaysPendingWhenLocalTaxPluginIsForOtherMarket(t *testing.T) {
	resetTaxCatalogForTest(t)
	_, dp := newRealDBDeps(t)
	initTestPaths(t)
	deadMarketplace(t, dp)
	seedLocalTaxPlugin(t, dp, "com.example.fr-tax", "FR")

	taxSpec := basePluginSpec{CanonicalType: "tax", Locale: "de"}
	if err := savePendingBasePlugins(t.Context(), dp, []basePluginSpec{taxSpec}); err != nil {
		t.Fatal(err)
	}

	basePluginRetryTick(t.Context(), dp)

	pending, err := loadPendingBasePlugins(t.Context(), dp)
	if err != nil {
		t.Fatalf("loadPendingBasePlugins: %v", err)
	}
	if len(pending) != 1 || pending[0] != taxSpec {
		t.Fatalf("pending after the tick = %+v, want %+v kept", pending, taxSpec)
	}
}

// The local check failing (here: plugin_markets gone) must not fail open into
// an unattended install — that would be the second fiscal plugin this card
// prevents. The spec stays pending for the next tick.
func TestBasePluginRetryTick_TaxSpecStaysPendingWhenLocalCheckErrors(t *testing.T) {
	resetTaxCatalogForTest(t)
	_, dp := newRealDBDeps(t)
	initTestPaths(t)
	mkt := newFakeMarketplace(t, map[string]string{"listing-tax-de": "ut-plugin-tax-de"})
	mkt.setCatalog(deTaxCatalogEntry("listing-tax-de", "ut-plugin-tax-de", "1.0.0"))
	dp.Cfg.Marketplace = mkt.config()
	seedLocalTaxPlugin(t, dp, "com.example.sideloaded-tax")
	if _, err := dp.Db.ExecContext(t.Context(), `DROP TABLE plugin_markets`); err != nil {
		t.Fatalf("drop plugin_markets: %v", err)
	}

	taxSpec := basePluginSpec{CanonicalType: "tax", Locale: "de"}
	if err := savePendingBasePlugins(t.Context(), dp, []basePluginSpec{taxSpec}); err != nil {
		t.Fatal(err)
	}

	basePluginRetryTick(t.Context(), dp)

	pending, err := loadPendingBasePlugins(t.Context(), dp)
	if err != nil {
		t.Fatalf("loadPendingBasePlugins: %v", err)
	}
	if len(pending) != 1 || pending[0] != taxSpec {
		t.Fatalf("pending after the tick = %+v, want %+v kept", pending, taxSpec)
	}
	if n := mkt.catalogHitsFor(""); n != 0 {
		t.Fatalf("catalog was browsed %d time(s) after the local check failed", n)
	}
	if active, _ := data.NewPluginRepo(dp.Db).PluginActive(t.Context(), "ut-plugin-tax-de"); active {
		t.Fatal("a failed local check must not lead to a second fiscal install")
	}
}

// --- ut-docs#3244: step 3 keeps the queued note without a catalog match ---

// #3210 keeps a consented tax/de spec pending when the catalog is reachable
// but publishes no DE tax listing. The tile is rendered only from a catalog
// match, so step 3 used to say nothing about the fiscal plugin at all; it
// must still show the queued note (and no Install button — there is nothing
// to install yet; the background retry already holds the consent).
func TestSetupGETShowsQueuedTaxNoteWhenCatalogHasNoListing(t *testing.T) {
	for _, tc := range []struct {
		name        string
		pending     []basePluginSpec
		sideloaded  bool
		wantPending bool
	}{
		{"pending tax spec", []basePluginSpec{{CanonicalType: "tax", Locale: "de"}}, false, true},
		{"nothing pending", nil, false, false},
		{"only a language spec pending", []basePluginSpec{{CanonicalType: "language", Locale: "de"}}, false, false},
		{"pending but a local tax plugin is active", []basePluginSpec{{CanonicalType: "tax", Locale: "de"}}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetTaxCatalogForTest(t)
			resetSetupLanguageCatalog()
			t.Cleanup(resetSetupLanguageCatalog)
			withOSLocale(t, "", "") // see TestSetupGETResumesStep3ForTaxCountry's comment
			mux, dp := newRealDBDeps(t)
			initTestPaths(t)
			mkt := newFakeMarketplace(t, map[string]string{}) // reachable, no DE tax listing
			dp.Cfg.Marketplace = mkt.config()
			if tc.sideloaded {
				seedLocalTaxPlugin(t, dp, "com.example.sideloaded-tax")
			}
			if err := savePendingBasePlugins(t.Context(), dp, tc.pending); err != nil {
				t.Fatal(err)
			}

			body := getSetup(mux, "", "").Body.String()
			if got := strings.Contains(body, "data-tax-plugin-pending"); got != tc.wantPending {
				t.Errorf("queued note shown = %v, want %v", got, tc.wantPending)
			}
			if strings.Contains(body, `action="/api/setup/tax-plugin"`) {
				t.Error("no catalog match: step 3 must not offer an Install button")
			}
		})
	}
}

// --- ut-docs#3243: say "not published yet", not "installing… if you're offline" ---

// A consented tax/de spec that the reachable catalog has no listing for is
// not "installing in the background … if you're offline" — the till is
// online and may wait days. The retry records why the spec is still pending,
// and the Settings chip says the plugin isn't in the catalog yet; an
// unreachable catalog keeps today's offline copy.
func TestSettingsPendingTaxChipSaysNotPublishedWhenCatalogHasNoListing(t *testing.T) {
	resetTaxCatalogForTest(t)
	mux, dp := newRealDBDeps(t)
	initTestPaths(t)
	taxSpec := basePluginSpec{CanonicalType: "tax", Locale: "de"}
	if err := savePendingBasePlugins(t.Context(), dp, []basePluginSpec{taxSpec}); err != nil {
		t.Fatal(err)
	}
	getSettings := func() string {
		req := httptest.NewRequest(http.MethodGet, "/settings", nil)
		req = auth.WithUser(req, mgrUser)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /settings = %d", rec.Code)
		}
		return rec.Body.String()
	}
	offlineCopy := "if you&#39;re offline"
	notPublishedCopy := "isn&#39;t in the plugin catalog yet"

	// Reachable catalog, no DE tax listing.
	mkt := newFakeMarketplace(t, map[string]string{})
	mkt.setCatalog()
	dp.Cfg.Marketplace = mkt.config()
	basePluginRetryTick(t.Context(), dp)
	body := getSettings()
	if !strings.Contains(body, notPublishedCopy) {
		t.Errorf("not-published tax spec: chip must say the plugin isn't in the catalog yet")
	}
	if strings.Contains(body, offlineCopy) {
		t.Errorf("not-published tax spec: chip must not claim the till may be offline")
	}

	// The catalog becomes unreachable: the next attempt's reason replaces
	// the recorded one, and the chip goes back to the offline copy.
	deadMarketplace(t, dp)
	basePluginRetryTick(t.Context(), dp)
	body = getSettings()
	if !strings.Contains(body, offlineCopy) {
		t.Errorf("unreachable catalog: chip must keep the offline copy")
	}
	if strings.Contains(body, notPublishedCopy) {
		t.Errorf("unreachable catalog: a stale not-published reason must be cleared")
	}
}

// Dismissing the pending spec forgets its recorded reason, so a later
// re-queue (country change, wizard re-run) never inherits a stale one.
func TestDismissPendingBasePluginForgetsNotPublishedReason(t *testing.T) {
	resetTaxCatalogForTest(t)
	_, dp := newRealDBDeps(t)
	initTestPaths(t)
	taxSpec := basePluginSpec{CanonicalType: "tax", Locale: "de"}
	if err := savePendingBasePlugins(t.Context(), dp, []basePluginSpec{taxSpec}); err != nil {
		t.Fatal(err)
	}
	mkt := newFakeMarketplace(t, map[string]string{})
	mkt.setCatalog()
	dp.Cfg.Marketplace = mkt.config()
	basePluginRetryTick(t.Context(), dp)
	if got := loadBasePluginsNotPublished(t.Context(), dp); !got[taxSpec] {
		t.Fatalf("expected tax/de recorded as not published, got %+v", got)
	}
	if err := dismissPendingBasePlugin(t.Context(), dp, "tax", "de"); err != nil {
		t.Fatal(err)
	}
	if got := loadBasePluginsNotPublished(t.Context(), dp); got[taxSpec] {
		t.Fatalf("dismiss must forget the not-published reason, got %+v", got)
	}
}

// Step 3's queued branch renders only when the catalog answered with no
// listing, so it says exactly that instead of "still installing".
func TestSetupGETQueuedTaxNoteSaysNotPublished(t *testing.T) {
	resetTaxCatalogForTest(t)
	resetSetupLanguageCatalog()
	t.Cleanup(resetSetupLanguageCatalog)
	withOSLocale(t, "", "") // see TestSetupGETResumesStep3ForTaxCountry's comment
	mux, dp := newRealDBDeps(t)
	initTestPaths(t)
	mkt := newFakeMarketplace(t, map[string]string{}) // reachable, no DE tax listing
	dp.Cfg.Marketplace = mkt.config()
	if err := savePendingBasePlugins(t.Context(), dp, []basePluginSpec{{CanonicalType: "tax", Locale: "de"}}); err != nil {
		t.Fatal(err)
	}
	body := getSetup(mux, "", "").Body.String()
	if !strings.Contains(body, "isn&#39;t in the plugin catalog yet") {
		t.Error("queued note without a catalog match must say the plugin isn't in the catalog yet")
	}
	if strings.Contains(body, "Still installing the tax plugin") {
		t.Error("queued note without a catalog match must not say it is still installing")
	}
}

// An offline re-render of step 3 after the operator already consented (the
// spec is on the pending list, no tax_plugin_pending query param — e.g. a
// reload) shows the queued note, not a fresh "Install when online" offer
// with no sign the consent was kept.
func TestSetupGETOfflineRerenderAfterConsentShowsQueuedNote(t *testing.T) {
	resetTaxCatalogForTest(t)
	withOSLocale(t, "", "") // see TestSetupGETResumesStep3ForTaxCountry's comment
	mux, dp := newRealDBDeps(t)
	initTestPaths(t)
	deadMarketplace(t, dp)
	if err := savePendingBasePlugins(t.Context(), dp, []basePluginSpec{{CanonicalType: "tax", Locale: "de"}}); err != nil {
		t.Fatal(err)
	}
	body := getSetup(mux, "?tax_country=DE", "").Body.String()
	if !strings.Contains(body, "data-tax-plugin-queued") {
		t.Error("offline re-render after consent must show the queued note")
	}
	if strings.Contains(body, "data-tax-plugin-offline") {
		t.Error("the queued note replaces the offline note, not both")
	}
}

// Review finding (ut-docs#3243): an attempt that was already in flight when
// the operator dismissed the spec must not leave a "not published" reason
// behind, and re-queuing a spec starts with no recorded reason — otherwise
// an offline re-queue would show "isn't in the catalog yet" before any
// attempt was made.
func TestNotPublishedReasonNotOrphanedByDismissRaceOrInheritedOnRequeue(t *testing.T) {
	resetTaxCatalogForTest(t)
	_, dp := newRealDBDeps(t)
	initTestPaths(t)
	taxSpec := basePluginSpec{CanonicalType: "tax", Locale: "de"}

	// The attempt finishes after the spec was dismissed: nothing is recorded.
	recordBasePluginAttempt(t.Context(), dp, taxSpec, errBasePluginNotPublished)
	if got := loadBasePluginsNotPublished(t.Context(), dp); got[taxSpec] {
		t.Fatalf("a not-pending spec must not be recorded as not published, got %+v", got)
	}

	// A stale reason already on disk is dropped when the spec is queued again.
	if err := savePendingBasePlugins(t.Context(), dp, []basePluginSpec{taxSpec}); err != nil {
		t.Fatal(err)
	}
	recordBasePluginAttempt(t.Context(), dp, taxSpec, errBasePluginNotPublished)
	if got := loadBasePluginsNotPublished(t.Context(), dp); !got[taxSpec] {
		t.Fatalf("a pending spec must be recorded, got %+v", got)
	}
	if err := savePendingBasePlugins(t.Context(), dp, nil); err != nil { // dropped without the dismiss path
		t.Fatal(err)
	}
	if err := addPendingBasePlugins(t.Context(), dp, []basePluginSpec{taxSpec}); err != nil {
		t.Fatal(err)
	}
	if got := loadBasePluginsNotPublished(t.Context(), dp); got[taxSpec] {
		t.Fatalf("re-queuing must clear a stale not-published reason, got %+v", got)
	}

	// A cancelled attempt (shutdown mid-fetch) leaves a correct reason alone.
	recordBasePluginAttempt(t.Context(), dp, taxSpec, errBasePluginNotPublished)
	recordBasePluginAttempt(t.Context(), dp, taxSpec, fmt.Errorf("%w: %w", errBasePluginCatalogUnreachable, context.Canceled))
	if got := loadBasePluginsNotPublished(t.Context(), dp); !got[taxSpec] {
		t.Fatalf("a cancelled attempt must not clear the recorded reason, got %+v", got)
	}
	// A genuinely unreachable catalog does clear it.
	recordBasePluginAttempt(t.Context(), dp, taxSpec, fmt.Errorf("%w: %w", errBasePluginCatalogUnreachable, errors.New("dial tcp: connection refused")))
	if got := loadBasePluginsNotPublished(t.Context(), dp); got[taxSpec] {
		t.Fatalf("an unreachable catalog must clear the reason, got %+v", got)
	}
}
