package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
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

// Catalog unreachable with nothing cached: catalogUnavailable=true (distinct
// from the unmapped-country case above), and no match.
func TestSetupInstallableTaxPlugin_CatalogUnreachableReportsUnavailable(t *testing.T) {
	resetTaxCatalogForTest(t)
	dp := newBasePluginTestDeps(t)
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	dp.Cfg.Marketplace.EndpointURL = dead.URL
	dp.Cfg.Marketplace.ClientID = "merchant-1"
	dp.Cfg.Marketplace.StoreID = "store-1"
	dead.Close()

	plugin, unavailable := setupInstallableTaxPlugin(t.Context(), dp, "DE")
	if plugin != nil {
		t.Fatalf("expected no match while unreachable, got %+v", plugin)
	}
	if !unavailable {
		t.Fatal("expected catalogUnavailable=true when the catalog cannot be reached and nothing is cached")
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

	// A non-DE country must not show the tile at all.
	form["country"] = []string{"FR"}
	rec = postFormRaw(mux, "/api/setup", form)
	if strings.Contains(rec.Body.String(), `action="/api/setup/tax-plugin"`) {
		t.Error("a non-DE country must not show the tax-plugin install tile")
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
