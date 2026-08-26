package pages

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#1092: the setup wizard's first screen must list every language the
// product can run in — the bundled locales AND every marketplace
// canonical_type=language catalog listing — and picking a catalog-only one
// must install its pack (Ed25519-verified, the existing path) and continue
// the wizard in that language. These tests cover the catalog cache, the tile
// dedup, and the new POST /api/setup/language handler.

// resetSetupLanguageCatalog clears the package-level catalog cache before AND
// after a test — it's process-global state (deliberately, it's a TTL cache),
// so without this a warm cache from one test would leak tiles into another
// test's GET /setup render, order-dependently.
func resetSetupLanguageCatalog(t *testing.T) {
	t.Helper()
	clear := func() {
		setupLanguageCatalog.mu.Lock()
		setupLanguageCatalog.locales = nil
		setupLanguageCatalog.hasData = false
		setupLanguageCatalog.fetchedAt = time.Time{}
		setupLanguageCatalog.mu.Unlock()
	}
	clear()
	t.Cleanup(clear)
}

// countingLanguageCatalogServer serves GET /v1/catalog/plugins in the REAL
// ut-cloud wire shape (camelCase availableLocales, plain `type` — the same
// shape fakeMarketplace serves, see its own #1055 comment) and counts hits,
// so the TTL tests can prove a cached render made no network call at all.
type countingLanguageCatalogServer struct {
	server *httptest.Server
	mu     sync.Mutex
	hits   int
}

func newCountingLanguageCatalogServer(t *testing.T, entries []map[string]any) *countingLanguageCatalogServer {
	t.Helper()
	s := &countingLanguageCatalogServer{}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/catalog/plugins" {
			http.NotFound(w, r)
			return
		}
		s.mu.Lock()
		s.hits++
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"plugins": entries})
	}))
	t.Cleanup(s.server.Close)
	return s
}

func (s *countingLanguageCatalogServer) hitCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits
}

func (s *countingLanguageCatalogServer) config() config.MarketplaceConfig {
	return config.MarketplaceConfig{EndpointURL: s.server.URL, RequestTimeoutSec: 5}
}

// --- setupCatalogLanguageLocales: fetch, TTL, stale fallback ---------------

func TestSetupCatalogLanguageLocales_FetchFiltersAndNormalizes(t *testing.T) {
	resetSetupLanguageCatalog(t)
	srv := newCountingLanguageCatalogServer(t, []map[string]any{
		// A region-suffixed language listing must normalize to its base tag.
		{"id": "p-de", "name": "German", "version": "1.0.0", "type": "language", "availableLocales": []string{"de-DE"}},
		// A multi-locale listing contributes every declared locale.
		{"id": "p-es", "name": "Spanish", "version": "1.0.0", "type": "language", "availableLocales": []string{"es", "ca"}},
		// A non-language listing must be filtered out client-side even though
		// the request asked the server to filter (same no-trust posture as
		// resolveAndInstallBasePlugin).
		{"id": "p-theme", "name": "Theme", "version": "1.0.0", "type": "theme", "availableLocales": []string{"fr"}},
	})
	d := &common.Deps{Cfg: &config.Config{Marketplace: srv.config()}}

	locales, ok := setupCatalogLanguageLocales(t.Context(), d)
	if !ok {
		t.Fatal("expected a successful catalog fetch")
	}
	want := []string{"ca", "de", "es"}
	if len(locales) != len(want) {
		t.Fatalf("locales = %v, want %v", locales, want)
	}
	for i := range want {
		if locales[i] != want[i] {
			t.Fatalf("locales = %v, want %v", locales, want)
		}
	}
}

// The catalog is an UNSIGNED marketplace response (Ed25519 only covers the
// plugin artifact itself, never this listing metadata) — a hostile or
// buggy listing's availableLocales must never reach the render pipeline
// unvalidated. Found by independent review: the template drops each
// catalog locale straight into an Alpine `@click`/`:aria-busy` JS-attribute
// expression, which html/template escapes as HTML text, not as JavaScript
// — a value like `a'+alert(1)+'` HTML-decodes back into a real quote before
// Alpine evaluates it, breaking out of the string literal on a page that
// collects the admin PIN two steps later. The fix is the same
// isBareLocaleCode gate already used to validate setup_page.go's own
// user/query-controlled locale values, applied here too.
func TestSetupCatalogLanguageLocales_RejectsNonBareLocaleCode(t *testing.T) {
	resetSetupLanguageCatalog(t)
	srv := newCountingLanguageCatalogServer(t, []map[string]any{
		{"id": "p-good", "name": "German", "version": "1.0.0", "type": "language", "availableLocales": []string{"de"}},
		// A hostile/malformed listing must never survive into the cache.
		{"id": "p-evil", "name": "Evil", "version": "1.0.0", "type": "language", "availableLocales": []string{"a'+alert(1)+'"}},
		// Also reject anything that's merely too long/short/non-letters —
		// isBareLocaleCode's own bounds, not just quote characters.
		{"id": "p-long", "name": "Long", "version": "1.0.0", "type": "language", "availableLocales": []string{"toolongtag"}},
		{"id": "p-digit", "name": "Digit", "version": "1.0.0", "type": "language", "availableLocales": []string{"d3"}},
	})
	d := &common.Deps{Cfg: &config.Config{Marketplace: srv.config()}}

	locales, ok := setupCatalogLanguageLocales(t.Context(), d)
	if !ok {
		t.Fatal("expected a successful catalog fetch")
	}
	want := []string{"de"}
	if len(locales) != len(want) || locales[0] != want[0] {
		t.Fatalf("locales = %v, want %v (every non-bare-locale-code entry must be dropped)", locales, want)
	}
}

func TestSetupCatalogLanguageLocales_TTLServesCacheWithoutRefetch(t *testing.T) {
	resetSetupLanguageCatalog(t)
	srv := newCountingLanguageCatalogServer(t, []map[string]any{
		{"id": "p-de", "name": "German", "version": "1.0.0", "type": "language", "availableLocales": []string{"de"}},
	})
	d := &common.Deps{Cfg: &config.Config{Marketplace: srv.config()}}

	if _, ok := setupCatalogLanguageLocales(t.Context(), d); !ok {
		t.Fatal("first fetch failed")
	}
	if _, ok := setupCatalogLanguageLocales(t.Context(), d); !ok {
		t.Fatal("second (cached) read failed")
	}
	if hits := srv.hitCount(); hits != 1 {
		t.Fatalf("catalog hit %d times inside the TTL, want exactly 1 (cache must serve the second read)", hits)
	}
}

func TestSetupCatalogLanguageLocales_StaleCacheServedWhenCatalogUnreachable(t *testing.T) {
	resetSetupLanguageCatalog(t)
	srv := newCountingLanguageCatalogServer(t, []map[string]any{
		{"id": "p-de", "name": "German", "version": "1.0.0", "type": "language", "availableLocales": []string{"de"}},
	})
	d := &common.Deps{Cfg: &config.Config{Marketplace: srv.config()}}
	if _, ok := setupCatalogLanguageLocales(t.Context(), d); !ok {
		t.Fatal("first fetch failed")
	}

	// The catalog goes away AND the TTL expires: the stale cache must still
	// be served (ok=true) rather than dropping the tiles mid-setup.
	srv.server.Close()
	setupLanguageCatalog.mu.Lock()
	setupLanguageCatalog.fetchedAt = time.Now().Add(-setupLanguageCatalogTTL - time.Second)
	setupLanguageCatalog.mu.Unlock()

	locales, ok := setupCatalogLanguageLocales(t.Context(), d)
	if !ok || len(locales) != 1 || locales[0] != "de" {
		t.Fatalf("stale-cache read = (%v, %v), want ([de], true)", locales, ok)
	}
}

func TestSetupCatalogLanguageLocales_ColdCacheOfflineFailsClean(t *testing.T) {
	resetSetupLanguageCatalog(t)
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	cfg := config.MarketplaceConfig{EndpointURL: dead.URL, RequestTimeoutSec: 5}
	dead.Close()
	d := &common.Deps{Cfg: &config.Config{Marketplace: cfg}}

	locales, ok := setupCatalogLanguageLocales(t.Context(), d)
	if ok || len(locales) != 0 {
		t.Fatalf("cold-cache offline read = (%v, %v), want (nil, false)", locales, ok)
	}
}

// --- dedup against the locales the till already serves ---------------------

func TestSetupCatalogOnlyLanguages_DedupsAgainstAvailable(t *testing.T) {
	got := setupCatalogOnlyLanguages([]string{"de", "tr", "es", "en"}, []string{"en", "tr", "fa", "ar"})
	want := []string{"de", "es"}
	if len(got) != len(want) {
		t.Fatalf("catalog-only = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("catalog-only = %v, want %v", got, want)
		}
	}

	// Base-language matching both ways: an installed "de" covers a catalog
	// "de-DE" and vice versa — no redundant tile either way round.
	if got := setupCatalogOnlyLanguages([]string{"de-DE"}, []string{"en", "de"}); len(got) != 0 {
		t.Fatalf("catalog de-DE with de installed should dedup to nothing, got %v", got)
	}
	if got := setupCatalogOnlyLanguages([]string{"de"}, []string{"en", "de-DE"}); len(got) != 0 {
		t.Fatalf("catalog de with de-DE installed should dedup to nothing, got %v", got)
	}
}

// --- GET /setup: the tile list itself --------------------------------------

// The wizard <form> must carry a disabled, hidden guard submit button
// BEFORE anything else — found by independent review: every step's
// <section> stays in the DOM the whole time (x-show, not x-if), so once a
// catalog install tile (a real submit button) exists on step 1, it becomes
// the browser's implicit "Enter in a text field" target for EVERY later
// step too (shop name, TSE fields, the PIN) — silently firing an
// unintended language install and discarding whatever the operator had
// typed. This is a markup-presence regression pin, not a browser-behavior
// test (Go's httptest never executes implicit-submission semantics); the
// actual Enter-key behavior was verified against a real running till with
// a real browser (see the ut-docs#1092 code-review record).
func TestSetupWizardFormHasImplicitSubmitGuardBeforeCatalogTiles(t *testing.T) {
	resetSetupLanguageCatalog(t)
	mux, _, d := newFullAuthDeps(t)
	srv := newCountingLanguageCatalogServer(t, []map[string]any{
		{"id": "p-de", "name": "German", "version": "1.1.0", "type": "language", "availableLocales": []string{"de"}},
	})
	d.Cfg.Marketplace = srv.config()

	rec := getSetup(mux, "?lang=en", "")
	body := rec.Body.String()
	guardIdx := strings.Index(body, "data-implicit-submit-guard")
	tileIdx := strings.Index(body, `formaction="/api/setup/language"`)
	formIdx := strings.Index(body, "<form")
	if guardIdx < 0 {
		t.Fatal("wizard form is missing the implicit-submission guard button")
	}
	if tileIdx < 0 {
		t.Fatal("expected a catalog install tile to exist for this assertion to be meaningful")
	}
	if guardIdx > tileIdx {
		t.Fatalf("guard button (offset %d) must render BEFORE the catalog tile (offset %d) to become the form's default submit target", guardIdx, tileIdx)
	}
	if guardIdx-formIdx > 400 {
		t.Errorf("guard button sits %d bytes after <form> — it must be the form's first child, not buried after other markup", guardIdx-formIdx)
	}
	if !strings.Contains(body, `<button type="submit" disabled hidden`) {
		t.Error("guard button must be disabled+hidden — a real, clickable button here would itself be a stray control")
	}
}

// The step-1 language tiles list every language the product can run in:
// bundled locales AND every marketplace canonical_type=language catalog
// listing (deduped — a catalog listing whose locale the till already serves
// (tr is a core locale) gets no redundant second tile.
func TestSetupWizardListsCatalogLanguages(t *testing.T) {
	resetSetupLanguageCatalog(t)
	mux, _, d := newFullAuthDeps(t)
	srv := newCountingLanguageCatalogServer(t, []map[string]any{
		{"id": "p-de", "name": "German", "version": "1.1.0", "type": "language", "availableLocales": []string{"de"}},
		{"id": "p-tr", "name": "Turkish", "version": "1.0.0", "type": "language", "availableLocales": []string{"tr"}},
	})
	d.Cfg.Marketplace = srv.config()

	rec := getSetup(mux, "?lang=en", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /setup?lang=en: code=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `formaction="/api/setup/language"`) {
		t.Fatal("step 1 has no install tile posting to /api/setup/language")
	}
	if !strings.Contains(body, `data-catalog-lang="de"`) {
		t.Fatal("step 1 is missing the catalog-only German tile")
	}
	if strings.Contains(body, `data-catalog-lang="tr"`) {
		t.Fatal("tr is a bundled locale — it must not get a redundant catalog tile")
	}
	// The bundled links themselves are unchanged.
	if !strings.Contains(body, `href="/setup?lang=tr"`) {
		t.Fatal("bundled locale links must still render")
	}
	// Catalog reachable: no offline note.
	if strings.Contains(body, "data-catalog-unavailable") {
		t.Fatal("catalog-unavailable note shown while the catalog is reachable")
	}
}

// Offline-first: with the catalog unreachable and nothing cached, step 1
// still renders the bundled locales and says plainly that more are available
// once connected — it never blocks and never errors.
func TestSetupWizardOfflineCatalogShowsBundledPlusNote(t *testing.T) {
	resetSetupLanguageCatalog(t)
	mux, _, d := newFullAuthDeps(t)
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	d.Cfg.Marketplace = config.MarketplaceConfig{EndpointURL: dead.URL, RequestTimeoutSec: 5}
	dead.Close()

	rec := getSetup(mux, "?lang=en", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /setup?lang=en offline: code=%d, want 200 (offline must never block setup)", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "data-catalog-unavailable") {
		t.Fatal("offline step 1 must carry the more-languages-once-connected note")
	}
	if strings.Contains(body, `formaction="/api/setup/language"`) {
		t.Fatal("offline step 1 must not render catalog install tiles it cannot honour")
	}
	for _, lang := range []string{"en", "tr", "fa", "ar"} {
		if !strings.Contains(body, `href="/setup?lang=`+lang+`"`) {
			t.Errorf("offline step 1 is missing the bundled %s link", lang)
		}
	}
}

// The install-pending redirect target (?install_pending=<locale>) renders the
// "still installing X — continuing in Y for now" note; a garbage value is
// dropped rather than echoed.
func TestSetupWizardRendersInstallPendingNote(t *testing.T) {
	resetSetupLanguageCatalog(t)
	mux, _, _ := newFullAuthDeps(t)

	rec := getSetup(mux, "?lang=en&install_pending=de", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /setup?lang=en&install_pending=de: code=%d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `data-install-pending="de"`) {
		t.Fatal("install-pending note missing after a failed foreground install redirect")
	}

	rec = getSetup(mux, "?lang=en&install_pending="+url.QueryEscape(`"><script>`), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /setup with garbage install_pending: code=%d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "data-install-pending=") {
		t.Fatal("a non-locale install_pending value must be dropped, not rendered")
	}
}

// The country step (installBasePluginsForSetup, ut-docs#591) must MERGE
// into the pending list, never wholesale-replace it — found by independent
// review. Before this fix: an operator picks a catalog-only language at
// step 1 (fails offline, queued for retry), then confirms a country whose
// own free base plugin also needs installing; the country step's pending
// list write silently dropped the step-1 language spec, breaking the
// "we'll keep trying in the background" promise the install-pending note
// makes (setup.language.install_retry_hint) — never a silent fallback is
// the whole point of that queue.
func TestInstallBasePluginsForSetup_MergesWithLanguageStepPending(t *testing.T) {
	mux, d := newSetupLanguageHandlerDeps(t)
	_ = mux
	// Step 1: the operator picked a catalog-only Spanish pack, offline —
	// already queued, exactly like TestSetupLanguageInstall_OfflineLeavesPendingForRetry.
	esSpec := basePluginSpec{CanonicalType: "language", Locale: "es"}
	if err := addPendingBasePlugin(t.Context(), d, esSpec); err != nil {
		t.Fatalf("addPendingBasePlugin(es): %v", err)
	}

	// The country step then confirms Germany, offline too (nothing
	// published — same shape as TestSetupWizardDE_OfflineCompletesAndLeavesPendingForRetry).
	installBasePluginsForSetup(t.Context(), d, "DE")

	pending, err := loadPendingBasePlugins(t.Context(), d)
	if err != nil {
		t.Fatalf("loadPendingBasePlugins: %v", err)
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

// The detected-but-core-unavailable "coming soon" note and a working
// catalog-install tile for that SAME language must never render together —
// found by independent review: on a real German Pi with the marketplace
// German pack published, step 1 said "We don't have de yet — it's on the
// way" directly above a tile that installs de right now. That's the exact
// field report this card exists to fix (ut-docs#1092's own "it doesn't have
// german language to select"), reproduced by a different mechanism. The
// note (and the setup.detected_lang_unavailable telemetry it's paired
// with — ut-docs#589 child 3's missing-language ticket-filer) must only
// fire for a language genuinely absent everywhere, catalog included.
func TestSetupWizardCatalogAvailableLanguageSuppressesComingSoonNote(t *testing.T) {
	resetSetupLanguageCatalog(t)
	mux, _, d := newFullAuthDeps(t)
	withOSLocale(t, "de_DE.UTF-8", "Europe/Berlin")
	srv := newCountingLanguageCatalogServer(t, []map[string]any{
		{"id": "p-de", "name": "German", "version": "1.1.0", "type": "language", "availableLocales": []string{"de"}},
	})
	d.Cfg.Marketplace = srv.config()

	rec := getSetup(mux, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /setup (de_DE, catalog offers de): code=%d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, `data-detected-lang="de"`) {
		t.Error(`step 1 shows the "we don't have de yet" note while a de install tile is offered on the same screen`)
	}
	if !strings.Contains(body, `data-catalog-lang="de"`) {
		t.Error("step 1 is missing the de catalog tile the note contradicts")
	}
	if v, ok, _ := d.Settings.Get(t.Context(), "setup.detected_lang_unavailable"); ok {
		t.Errorf(`setup.detected_lang_unavailable = %q, want unset — de is available via the catalog, not genuinely missing`, v)
	}
}

// --- POST /api/setup/language ----------------------------------------------

// newSetupLanguageHandlerDeps is the full fixture the install handler needs:
// a real migrated DB (the install path's plugins/plugin_catalog/
// plugin_install_status tables), scoped plugin paths, and the setup routes.
func newSetupLanguageHandlerDeps(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	resetSetupLanguageCatalog(t)
	mux, d := newRealDBDeps(t)
	initTestPaths(t)
	return mux, d
}

// warmSetupLanguageCatalog primes the package cache from whatever catalog
// d.Cfg.Marketplace currently points at — the POST handler validates the
// posted locale against this cache, exactly as a real wizard render would
// have populated it before the operator could tap a tile.
func warmSetupLanguageCatalog(t *testing.T, d *common.Deps) {
	t.Helper()
	if _, ok := setupCatalogLanguageLocales(t.Context(), d); !ok {
		t.Fatal("warming the setup language catalog cache failed")
	}
}

func TestSetupLanguageInstall_SuccessInstallsAndContinuesInLocale(t *testing.T) {
	mux, d := newSetupLanguageHandlerDeps(t)
	mkt := newFakeMarketplace(t, map[string]string{"listing-lang-de": "ut-plugin-language-de"})
	mkt.setCatalog(deLanguageCatalogEntry("listing-lang-de", "ut-plugin-language-de", "1.0.0"))
	d.Cfg.Marketplace = mkt.config()
	warmSetupLanguageCatalog(t, d)

	rec := postForm(mux, "/api/setup/language", url.Values{"locale": {"de"}}, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/setup?lang=de" {
		t.Fatalf("install POST: code=%d loc=%q body=%s, want 303 -> /setup?lang=de",
			rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	if active, err := data.NewPluginRepo(d.Db).PluginActive(t.Context(), "ut-plugin-language-de"); err != nil || !active {
		t.Fatalf("expected the German pack installed by the foreground handler: active=%v err=%v", active, err)
	}
	if pending, _ := loadPendingBasePlugins(t.Context(), d); len(pending) != 0 {
		t.Fatalf("nothing should be left pending after a successful foreground install, got %+v", pending)
	}
	// The Ed25519-verified install path was really used (download-token flow).
	if hits := mkt.downloadTokenHits(); hits != 1 {
		t.Fatalf("expected exactly one download-token request, got %d", hits)
	}
}

// A real install failure (the catalog lists the pack but the download 404s)
// must not strand the operator: the spec joins the existing background retry
// (ut-docs#591 infra) and the redirect says so — never a silent fallback.
func TestSetupLanguageInstall_FailureLeavesPendingAndExplains(t *testing.T) {
	mux, d := newSetupLanguageHandlerDeps(t)
	mkt := newFakeMarketplace(t, nil) // nothing published: download token will 404
	mkt.setCatalog(deLanguageCatalogEntry("listing-lang-de", "ut-plugin-language-de", "1.0.0"))
	d.Cfg.Marketplace = mkt.config()
	warmSetupLanguageCatalog(t, d)

	rec := postForm(mux, "/api/setup/language", url.Values{"locale": {"de"}}, nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("failed install POST: code=%d body=%s, want 303", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "install_pending=de") {
		t.Fatalf("redirect %q does not carry install_pending=de — the wizard would fall back silently", loc)
	}
	if !strings.HasPrefix(loc, "/setup?lang=") {
		t.Fatalf("redirect %q must continue the wizard in an available fallback locale", loc)
	}
	pending, err := loadPendingBasePlugins(t.Context(), d)
	if err != nil {
		t.Fatalf("loadPendingBasePlugins: %v", err)
	}
	if len(pending) != 1 || pending[0] != (basePluginSpec{CanonicalType: "language", Locale: "de"}) {
		t.Fatalf("expected the de spec queued for background retry, got %+v", pending)
	}
}

// Offline at install time (catalog was browsed earlier, network gone now):
// same pending-and-explain outcome, and the handler answers promptly.
func TestSetupLanguageInstall_OfflineLeavesPendingForRetry(t *testing.T) {
	mux, d := newSetupLanguageHandlerDeps(t)
	mkt := newFakeMarketplace(t, map[string]string{"listing-lang-de": "ut-plugin-language-de"})
	mkt.setCatalog(deLanguageCatalogEntry("listing-lang-de", "ut-plugin-language-de", "1.0.0"))
	d.Cfg.Marketplace = mkt.config()
	warmSetupLanguageCatalog(t, d)

	// The network goes away between the render and the tap: a closed local
	// server fails instantly (refused), so the test doesn't sit on the 20s
	// foreground timeout while still exercising a genuine unreachable path.
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadCfg := mkt.config()
	deadCfg.EndpointURL = dead.URL
	dead.Close()
	d.Cfg.Marketplace = deadCfg

	rec := postForm(mux, "/api/setup/language", url.Values{"locale": {"de"}}, nil)
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "install_pending=de") {
		t.Fatalf("offline install POST: code=%d loc=%q, want 303 with install_pending=de",
			rec.Code, rec.Header().Get("Location"))
	}
	pending, err := loadPendingBasePlugins(t.Context(), d)
	if err != nil {
		t.Fatalf("loadPendingBasePlugins: %v", err)
	}
	if len(pending) != 1 || pending[0].Locale != "de" {
		t.Fatalf("expected the de spec pending after an offline attempt, got %+v", pending)
	}

	// Network returns: the existing background retry tick installs it — the
	// wizard path and #1055's country path share one pending list.
	d.Cfg.Marketplace = mkt.config()
	basePluginRetryTick(t.Context(), d)
	if active, _ := data.NewPluginRepo(d.Db).PluginActive(t.Context(), "ut-plugin-language-de"); !active {
		t.Fatal("expected the background retry to install the wizard-requested pack")
	}
}

// A failed install's fallback must be a bare base-language code, not
// whatever region-tagged value ResolveLocale happens to return (the OS-
// detected default can be "en-US", never one of the wizard's own tile
// values). Found by a real driven browser run: the redirect landed on
// /setup?lang=en-US and the rendered note read "continuing in en-US for
// now" — inconsistent with every other tile/note in the wizard, which only
// ever shows bare codes ("en", "tr", "fa", "ar", "de").
func TestSetupLanguageInstall_FallbackNormalizesRegionTag(t *testing.T) {
	mux, d := newSetupLanguageHandlerDeps(t)
	mkt := newFakeMarketplace(t, nil) // nothing published: download token will 404
	mkt.setCatalog(deLanguageCatalogEntry("listing-lang-de", "ut-plugin-language-de", "1.0.0"))
	d.Cfg.Marketplace = mkt.config()
	warmSetupLanguageCatalog(t, d)

	req := httptest.NewRequest(http.MethodPost, "/api/setup/language", strings.NewReader(url.Values{"locale": {"de"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "ut_lang", Value: "en-US"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusSeeOther || loc != "/setup?lang=en&install_pending=de" {
		t.Fatalf("failed install with ut_lang=en-US: code=%d loc=%q, want 303 -> /setup?lang=en&install_pending=de", rec.Code, loc)
	}
}

// The POST body must not be able to install an arbitrary listing: a locale
// the cached catalog never offered is rejected outright.
func TestSetupLanguageInstall_RejectsLocaleNotInCatalog(t *testing.T) {
	mux, d := newSetupLanguageHandlerDeps(t)
	mkt := newFakeMarketplace(t, map[string]string{"listing-lang-de": "ut-plugin-language-de"})
	mkt.setCatalog(deLanguageCatalogEntry("listing-lang-de", "ut-plugin-language-de", "1.0.0"))
	d.Cfg.Marketplace = mkt.config()
	warmSetupLanguageCatalog(t, d)

	for _, locale := range []string{"fr", "", `de"]<script`, "zz"} {
		rec := postForm(mux, "/api/setup/language", url.Values{"locale": {locale}}, nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("locale %q: code=%d, want 400", locale, rec.Code)
		}
	}
	if pending, _ := loadPendingBasePlugins(t.Context(), d); len(pending) != 0 {
		t.Fatalf("a rejected locale must queue nothing, got %+v", pending)
	}
	if hits := mkt.downloadTokenHits(); hits != 0 {
		t.Fatalf("a rejected locale must trigger no download, got %d token hits", hits)
	}
}

// Same first-boot-only window as the rest of the wizard: once an operator
// exists, the auth-exempt install endpoint refuses and redirects.
func TestSetupLanguageInstall_RefusesAfterFirstBoot(t *testing.T) {
	resetSetupLanguageCatalog(t)
	mux, svc, d := newFullAuthDeps(t)
	_ = d

	id, err := svc.Repo().CreateUser(t.Context(), "boss", "Boss", "admin")
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := auth.HashPIN("1234")
	if err := svc.Repo().SetUserPIN(t.Context(), id, hash); err != nil {
		t.Fatal(err)
	}

	rec := postForm(mux, "/api/setup/language", url.Values{"locale": {"de"}}, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("POST /api/setup/language after first boot: code=%d loc=%q, want 303 -> /login",
			rec.Code, rec.Header().Get("Location"))
	}
}
