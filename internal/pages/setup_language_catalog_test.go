package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
)

// ut-docs#1092: the setup wizard's language step (step 1) also lists every
// marketplace catalog listing with canonical_type=language that isn't already
// covered by a bundled locale, and POST /api/setup/language installs one on
// selection through the EXACT ut-docs#591/#1055 install path
// (resolveAndInstallBasePlugin) — never a second install mechanism.

// resetLangCatalogForTest isolates each test from the package-level TTL cache
// — and, via Cleanup, keeps whatever this test cached from leaking into every
// other GET /setup this package renders within the 5-minute TTL.
func resetLangCatalogForTest(t *testing.T) {
	t.Helper()
	resetSetupLanguageCatalog()
	t.Cleanup(resetSetupLanguageCatalog)
}

// The catalog fetch populates the tile list with a not-yet-installed language
// (a real POST form, not htmx — this wizard is plain multi-page navigation),
// and a second render within the TTL serves from the cache without re-calling
// ListPlugins.
func TestSetupWizardListsInstallableCatalogLanguagesAndCachesFetch(t *testing.T) {
	resetLangCatalogForTest(t)
	mux, _, d := newFullAuthDeps(t)
	mkt := newFakeMarketplace(t, nil)
	mkt.setCatalog(deLanguageCatalogEntry("listing-lang-de", "ut-plugin-language-de", "1.0.0"))
	d.Cfg.Marketplace = mkt.config()

	rec := getSetup(mux, "?lang=en", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /setup?lang=en: code=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `action="/api/setup/language"`) {
		t.Fatalf("GET /setup body has no install form for the catalog language:\n%s", body)
	}
	if !strings.Contains(body, `name="locale" value="de"`) {
		t.Error("install form is missing its hidden locale=de input")
	}
	// Native display name, not the raw code or the listing's English name.
	if !strings.Contains(body, "Deutsch") {
		t.Error(`the de tile should carry the language's native display name ("Deutsch")`)
	}
	if strings.Contains(body, "data-lang-catalog-unavailable") {
		t.Error("catalog note shown even though the catalog was reachable")
	}

	// Within the TTL a re-render must serve from the cache.
	rec = getSetup(mux, "?lang=en", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("second GET /setup?lang=en: code=%d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `name="locale" value="de"`) {
		t.Error("second render lost the install tile")
	}
	if hits := mkt.catalogHits(); hits != 1 {
		t.Fatalf("expected exactly one catalog fetch across two renders (TTL cache), got %d", hits)
	}
}

// ADR-0015 (lazy store registration, still governing — ADR-0026's eager
// proposal was never accepted): rendering the wizard only BROWSES the
// catalog, and browsing must never mint the shop's cloud store identity.
// Only a plugin download/install (the POST below, via
// resolveAndInstallBasePlugin) or an operator's explicit Settings →
// "Register now" may enrol. Independent review caught GET /setup calling
// enroll.EnsureRegistered, which enrolled every till that ever reached its
// first screen — precisely the "every download, demo, test boot and CI run
// mints a store org" the ADR exists to prevent.
func TestSetupWizardCatalogBrowseDoesNotRegisterStore(t *testing.T) {
	resetLangCatalogForTest(t)
	mux, _, d := newFullAuthDeps(t)
	mkt := newFakeMarketplace(t, nil)
	mkt.setCatalog(deLanguageCatalogEntry("listing-lang-de", "ut-plugin-language-de", "1.0.0"))
	d.Cfg.Marketplace = mkt.config()

	rec := getSetup(mux, "?lang=en", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /setup?lang=en: code=%d", rec.Code)
	}
	// The browse really happened (otherwise "no register call" proves nothing).
	if hits := mkt.catalogHits(); hits != 1 {
		t.Fatalf("expected the catalog to be browsed once, got %d hits", hits)
	}
	if hits := mkt.storeRegisterHits(); hits != 0 {
		t.Fatalf("GET /setup enrolled the shop's cloud store (%d POST /v1/stores/register) — "+
			"ADR-0015 allows that only on a plugin download/install or an explicit Register now", hits)
	}
}

// Catalog unreachable at GET /setup: bundled-only rendering, no error, no
// hang — plus the "more languages once connected" note (mirrors
// TestSetupWizardDE_OfflineCompletesAndLeavesPendingForRetry's dead-server
// shape: connections fail immediately, so the render must too).
func TestSetupWizardCatalogUnreachableRendersBundledOnly(t *testing.T) {
	resetLangCatalogForTest(t)
	mux, _, d := newFullAuthDeps(t)
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	d.Cfg.Marketplace.EndpointURL = dead.URL
	d.Cfg.Marketplace.ClientID = "merchant-1"
	d.Cfg.Marketplace.StoreID = "store-1"
	dead.Close()

	start := time.Now()
	rec := getSetup(mux, "?lang=en", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /setup?lang=en with catalog unreachable: code=%d, want 200", rec.Code)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("GET /setup took %v with the catalog unreachable — the fetch is not bounded", elapsed)
	}
	body := rec.Body.String()
	if strings.Contains(body, `action="/api/setup/language"`) {
		t.Error("install tiles rendered with no catalog to back them")
	}
	if !strings.Contains(body, "data-lang-catalog-unavailable") {
		t.Error(`missing the "more languages available once connected" note`)
	}
	// The bundled tiles are untouched.
	if !strings.Contains(body, `href="/setup?lang=tr"`) {
		t.Error("bundled locale tiles must still render when the catalog is unreachable")
	}
}

// Dedup: a catalog listing whose locale is already covered by a bundled
// locale (base-language subtag match, so "tr-TR" is covered by core "tr")
// gets no redundant install tile; a non-language listing never gets one.
func TestSetupWizardDedupsCatalogLocalesAlreadyBundled(t *testing.T) {
	resetLangCatalogForTest(t)
	mux, _, d := newFullAuthDeps(t)
	mkt := newFakeMarketplace(t, nil)
	mkt.setCatalog(
		marketplace.PluginSummary{ID: "ut-plugin-language-tr", ListingID: "listing-lang-tr", Version: "1.0.0", CanonicalType: "language", AvailableLocales: []string{"tr-TR"}},
		marketplace.PluginSummary{ID: "ut-plugin-theme-es", ListingID: "listing-theme-es", Version: "1.0.0", CanonicalType: "theme", AvailableLocales: []string{"es"}},
		deLanguageCatalogEntry("listing-lang-de", "ut-plugin-language-de", "1.0.0"),
	)
	d.Cfg.Marketplace = mkt.config()

	rec := getSetup(mux, "?lang=en", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /setup?lang=en: code=%d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `name="locale" value="de"`) {
		t.Error("the genuinely-new de listing should have an install tile")
	}
	if strings.Contains(body, `name="locale" value="tr"`) {
		t.Error("a catalog locale already bundled (tr) must not get a duplicate install tile")
	}
	if strings.Contains(body, `name="locale" value="es"`) {
		t.Error("a non-language listing must never produce an install tile")
	}
	if !strings.Contains(body, `href="/setup?lang=tr"`) {
		t.Error("the bundled tr tile itself must stay")
	}
}

// POST /api/setup/language happy path: installs through the Ed25519-verified
// path, then redirects through the SAME /setup?lang= mechanism a bundled
// tile uses — so following the redirect sets the same ut_lang cookie and the
// resulting state is identical to picking an already-bundled locale.
func TestSetupLanguageInstallHappyPath(t *testing.T) {
	resetLangCatalogForTest(t)
	mux, d := newRealDBDeps(t)
	initTestPaths(t)
	mkt := newFakeMarketplace(t, map[string]string{"listing-lang-de": "ut-plugin-language-de"})
	mkt.setCatalog(deLanguageCatalogEntry("listing-lang-de", "ut-plugin-language-de", "1.0.0"))
	d.Cfg.Marketplace = mkt.config()

	rec := postForm(mux, "/api/setup/language", url.Values{"locale": {"de"}}, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/setup?lang=de" {
		t.Fatalf("POST /api/setup/language: code=%d loc=%q, want 303 -> /setup?lang=de",
			rec.Code, rec.Header().Get("Location"))
	}
	if active, err := data.NewPluginRepo(d.Db).PluginActive(t.Context(), "ut-plugin-language-de"); err != nil || !active {
		t.Fatalf("expected the de language pack installed: active=%v err=%v", active, err)
	}
	if pending, _ := loadPendingBasePlugins(t.Context(), d); len(pending) != 0 {
		t.Fatalf("nothing should be pending after a successful foreground install, got %+v", pending)
	}

	// Following the redirect sets the exact ut_lang cookie a bundled pick sets.
	follow := getSetup(mux, "?lang=de", "")
	var gotCookie string
	for _, c := range follow.Result().Cookies() {
		if c.Name == "ut_lang" {
			gotCookie = c.Value
		}
	}
	if gotCookie != "de" {
		t.Fatalf("following the redirect set ut_lang=%q, want %q (same state as a bundled pick)", gotCookie, "de")
	}
}

// A locale NOT present in the cached catalog is ignored — no install attempt,
// just a clean redirect back to the wizard (the request body must not get to
// pick an arbitrary listing).
func TestSetupLanguageInstallRejectsLocaleNotInCatalog(t *testing.T) {
	resetLangCatalogForTest(t)
	mux, d := newRealDBDeps(t)
	initTestPaths(t)
	mkt := newFakeMarketplace(t, map[string]string{"listing-lang-de": "ut-plugin-language-de"})
	mkt.setCatalog(deLanguageCatalogEntry("listing-lang-de", "ut-plugin-language-de", "1.0.0"))
	d.Cfg.Marketplace = mkt.config()

	rec := postForm(mux, "/api/setup/language", url.Values{"locale": {"fr"}}, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/setup" {
		t.Fatalf("POST with unknown locale: code=%d loc=%q, want 303 -> /setup",
			rec.Code, rec.Header().Get("Location"))
	}
	if hits := mkt.downloadTokenHits(); hits != 0 {
		t.Fatalf("an unknown locale must not trigger any install attempt, got %d download-token hits", hits)
	}
	if pending, _ := loadPendingBasePlugins(t.Context(), d); len(pending) != 0 {
		t.Fatalf("an unknown locale must not be queued for retry, got %+v", pending)
	}
}

// Install failure/timeout: the spec joins the EXISTING ut-docs#591 pending
// list (Settings chip + 5-minute background retry — no second retry
// mechanism), and the redirect carries install_pending so the wizard can say
// so. Once the marketplace is back, the existing retry tick completes it —
// same assertion shape as
// TestInstallBasePluginsForSetup_OfflineThenBackgroundRetryInstalls.
func TestSetupLanguageInstallFailureFallsBackToPendingRetry(t *testing.T) {
	resetLangCatalogForTest(t)
	mux, d := newRealDBDeps(t)
	initTestPaths(t)
	mkt := newFakeMarketplace(t, map[string]string{"listing-lang-de": "ut-plugin-language-de"})
	mkt.setCatalog(deLanguageCatalogEntry("listing-lang-de", "ut-plugin-language-de", "1.0.0"))
	d.Cfg.Marketplace = mkt.config()

	// The tile list was fetched while the shop was online...
	if langs, unavailable := setupInstallableLanguages(t.Context(), d); unavailable || len(langs) != 1 {
		t.Fatalf("priming the catalog cache: langs=%+v unavailable=%v", langs, unavailable)
	}

	// ...then the network goes away before the operator taps the tile. A
	// closed local server fails immediately (connection refused), so the test
	// never waits out setupWizardLanguageInstallTimeout's real 20s.
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	d.Cfg.Marketplace.EndpointURL = deadURL

	rec := postForm(mux, "/api/setup/language", url.Values{"locale": {"de"}}, nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST with install failing: code=%d, want 303", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "install_pending=de") || !strings.Contains(loc, "lang=en") {
		t.Fatalf("failure redirect = %q, want /setup?lang=en&install_pending=de", loc)
	}
	pending, err := loadPendingBasePlugins(t.Context(), d)
	if err != nil {
		t.Fatalf("loadPendingBasePlugins: %v", err)
	}
	if len(pending) != 1 || pending[0] != (basePluginSpec{CanonicalType: "language", Locale: "de"}) {
		t.Fatalf("expected the de spec pending for the background retry, got %+v", pending)
	}
	if active, _ := data.NewPluginRepo(d.Db).PluginActive(t.Context(), "ut-plugin-language-de"); active {
		t.Fatal("nothing should be installed while the marketplace is unreachable")
	}

	// The redirect target shows the still-installing note.
	follow := getSetup(mux, "?lang=en&install_pending=de", "")
	if follow.Code != http.StatusOK {
		t.Fatalf("GET redirect target: code=%d", follow.Code)
	}
	if !strings.Contains(follow.Body.String(), `data-install-pending="de"`) {
		t.Error("redirect target should carry the still-installing-in-background note")
	}

	// Network returns: the EXISTING background retry finishes the job.
	d.Cfg.Marketplace = mkt.config()
	basePluginRetryTick(t.Context(), d)
	if pending, _ := loadPendingBasePlugins(t.Context(), d); len(pending) != 0 {
		t.Fatalf("expected the pending list cleared after the retry tick, got %+v", pending)
	}
	if active, _ := data.NewPluginRepo(d.Db).PluginActive(t.Context(), "ut-plugin-language-de"); !active {
		t.Fatal("expected the de language pack installed by the existing retry tick")
	}
}

// The happy path again, but through the REAL auth middleware
// (auth.Middleware), not the bare mux — setup_pairing_test.go's pattern,
// same reasoning: /api/setup/language only works at all because
// internal/auth/middleware.go exempts it (a first-boot till has no
// operators, so no session can ever exist), and every bare-mux test in this
// file would keep passing if that exemption were dropped. Tester reproduced
// exactly that live: the route 401'd on the real app while all bare-mux
// tests stayed green.
func TestSetupLanguageInstallExemptFromAuthMiddleware(t *testing.T) {
	t.Setenv("UT_AUTH", "on")
	resetLangCatalogForTest(t)
	mux, d := newRealDBDeps(t)
	initTestPaths(t)
	mkt := newFakeMarketplace(t, map[string]string{"listing-lang-de": "ut-plugin-language-de"})
	mkt.setCatalog(deLanguageCatalogEntry("listing-lang-de", "ut-plugin-language-de", "1.0.0"))
	d.Cfg.Marketplace = mkt.config()
	h := auth.Middleware(mux, auth.NewService(d.Db))

	req := httptest.NewRequest(http.MethodPost, "/api/setup/language", strings.NewReader(url.Values{"locale": {"de"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("POST /api/setup/language 401'd behind the real auth middleware — " +
			"the route is missing from internal/auth/middleware.go's exempt() switch")
	}
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/setup?lang=de" {
		t.Fatalf("POST through real middleware: code=%d loc=%q, want 303 -> /setup?lang=de",
			rec.Code, rec.Header().Get("Location"))
	}
	if active, err := data.NewPluginRepo(d.Db).PluginActive(t.Context(), "ut-plugin-language-de"); err != nil || !active {
		t.Fatalf("expected the de language pack installed through the middleware-wrapped path: active=%v err=%v", active, err)
	}
}

// The endpoint shares the wizard's first-boot window: once an operator
// exists it must refuse (redirect to login), exactly like POST /api/setup.
func TestSetupLanguageInstallRefusedAfterFirstBoot(t *testing.T) {
	resetLangCatalogForTest(t)
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

	rec := postForm(mux, "/api/setup/language", url.Values{"locale": {"de"}}, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("POST /api/setup/language after first boot: code=%d loc=%q, want 303 -> /login",
			rec.Code, rec.Header().Get("Location"))
	}
}

// ut-docs#1110: the "we don't have de yet — it's on the way" note must never
// render alongside a working de install tile — the card's own headline
// scenario (a real German Pi where the pack IS published) reproduced by a
// second mechanism: detectLanguage() only ever checks the bundled/installed
// set, with no cross-reference to the catalog fetch renderWizard performs a
// few lines later in the exact same request.
func TestSetupWizardCatalogAvailableLanguageSuppressesComingSoonNote(t *testing.T) {
	resetLangCatalogForTest(t)
	mux, _, d := newFullAuthDeps(t)
	withOSLocale(t, "de_DE.UTF-8", "Europe/Berlin")
	mkt := newFakeMarketplace(t, nil)
	mkt.setCatalog(deLanguageCatalogEntry("listing-lang-de", "ut-plugin-language-de", "1.1.0"))
	d.Cfg.Marketplace = mkt.config()

	rec := getSetup(mux, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /setup (de_DE, catalog offers de): code=%d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, `data-detected-lang="de"`) {
		t.Error(`step 1 shows the "we don't have de yet" note while a de install tile is offered on the same screen`)
	}
	if !strings.Contains(body, `name="locale" value="de"`) {
		t.Error("step 1 is missing the de catalog install tile the note contradicts")
	}
	if v, ok, _ := d.Settings.Get(t.Context(), "setup.detected_lang_unavailable"); ok {
		t.Errorf(`setup.detected_lang_unavailable = %q, want unset — de is available via the catalog, not genuinely missing`, v)
	}
}

// ut-docs#1110: the country step's pending-plugin write must MERGE, never
// wholesale-replace — an operator can queue a catalog-only language at step
// 1 (offline, retried in the background) before confirming a country whose
// own free base plugin also needs installing, and installBasePluginsForSetup
// (the country step's ut-docs#591 hook, untouched by #545) still persists via
// a bare savePendingBasePlugins, dropping whatever step 1 already queued.
func TestInstallBasePluginsForSetup_MergesWithLanguageStepPending(t *testing.T) {
	resetLangCatalogForTest(t)
	_, _, d := newFullAuthDeps(t)

	// Step 1: the operator picked a catalog-only Spanish pack, offline —
	// already queued for retry.
	esSpec := basePluginSpec{CanonicalType: "language", Locale: "es"}
	if err := savePendingBasePlugins(t.Context(), d, []basePluginSpec{esSpec}); err != nil {
		t.Fatalf("seed pending es spec: %v", err)
	}

	// The country step then confirms Germany, itself offline (no marketplace
	// configured on this fixture — same shape as this file's other
	// catalog-unreachable cases).
	installBasePluginsForSetup(t.Context(), d, "DE")

	pending, err := loadPendingBasePlugins(t.Context(), d)
	if err != nil {
		t.Fatalf("loadPendingBasePlugins: %v", err)
	}
	// Exactly these two, no more (a plain len check — a same-spec-added-
	// twice regression would otherwise pass the membership loop below
	// silently, per independent review finding 2).
	if len(pending) != 2 {
		t.Fatalf("pending = %+v, want exactly [es, de] — no duplicates, nothing extra", pending)
	}
	want := map[basePluginSpec]bool{
		esSpec: false,
		{CanonicalType: "language", Locale: "de"}: false,
	}
	for _, s := range pending {
		if _, ok := want[s]; !ok {
			t.Fatalf("unexpected spec left pending: %+v (full list: %+v)", s, pending)
		}
		want[s] = true
	}
	for s, seen := range want {
		if !seen {
			t.Errorf("expected %+v still pending after the country step, got %+v — the country step clobbered another step's queued spec", s, pending)
		}
	}
}

// ut-docs#1110 review finding 1: the OTHER half of the merge fix — a
// successful install must drop only ITS OWN spec, never wholesale-replace
// the whole list — had zero coverage; a mutant reverting
// dismissPendingBasePlugin back to a bare savePendingBasePlugins(nil) passed
// the entire package. This seeds an unrelated language-step spec, makes the
// country step's own install actually SUCCEED (a reachable fake catalog),
// and asserts the unrelated spec survives while only the installed one is
// removed — the exact bug class this card exists to fix, on the success
// path this time instead of the offline path above.
func TestInstallBasePluginsForSetup_SuccessRemovesOnlyItsOwnSpecNotTheWholeList(t *testing.T) {
	d := newBasePluginTestDeps(t)
	mkt := newFakeMarketplace(t, map[string]string{"listing-lang-de": "ut-plugin-language-de"})
	mkt.setCatalog(deLanguageCatalogEntry("listing-lang-de", "ut-plugin-language-de", "1.0.0"))
	d.Cfg.Marketplace = mkt.config()

	// An unrelated spec (from the wizard's language step) already queued —
	// nothing here should ever install it or touch it.
	esSpec := basePluginSpec{CanonicalType: "language", Locale: "es"}
	if err := savePendingBasePlugins(t.Context(), d, []basePluginSpec{esSpec}); err != nil {
		t.Fatalf("seed pending es spec: %v", err)
	}

	installBasePluginsForSetup(t.Context(), d, "DE")

	if active, err := data.NewPluginRepo(d.Db).PluginActive(t.Context(), "ut-plugin-language-de"); err != nil || !active {
		t.Fatalf("expected the German pack installed: active=%v err=%v", active, err)
	}
	pending, err := loadPendingBasePlugins(t.Context(), d)
	if err != nil {
		t.Fatalf("loadPendingBasePlugins: %v", err)
	}
	if len(pending) != 1 || pending[0] != esSpec {
		t.Fatalf("pending after a successful DE install = %+v, want exactly [es] — installing de must remove only de, never the whole list", pending)
	}
}
