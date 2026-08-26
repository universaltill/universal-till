package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
)

// newBasePluginTestDeps is newMigratedSyncDeps (real, fully migrated schema —
// resolveAndInstallBasePlugin's install path needs the real plugins/
// plugin_catalog/plugin_install_status tables) plus its own scoped plugin
// files directory, so an install never lands in the repo tree.
func newBasePluginTestDeps(t *testing.T) *common.Deps {
	t.Helper()
	initTestPaths(t)
	return newMigratedSyncDeps(t, "base_plugins.db")
}

// deLanguageCatalogEntry is the DE listing a real marketplace would serve:
// canonical type "language", availableLocales ["de"] — exactly what
// ut-docs#591's registry maps DE to. The fake serves this in the real
// ut-cloud wire shape (camelCase `availableLocales`), not the singular
// `locale` field the server never sent (#1055).
func deLanguageCatalogEntry(listingID, pluginID, version string) marketplace.PluginSummary {
	return marketplace.PluginSummary{
		ID: pluginID, ListingID: listingID, Name: "German language pack",
		Version: version, CanonicalType: "language", AvailableLocales: []string{"de"},
	}
}

// --- resolveAndInstallBasePlugin ---

func TestResolveAndInstallBasePlugin_DEHappyPath(t *testing.T) {
	dp := newBasePluginTestDeps(t)
	mkt := newFakeMarketplace(t, map[string]string{"listing-lang-de": "ut-plugin-language-de"})
	mkt.setCatalog(deLanguageCatalogEntry("listing-lang-de", "ut-plugin-language-de", "1.0.0"))
	dp.Cfg.Marketplace = mkt.config()

	spec := basePluginSpec{CanonicalType: "language", Locale: "de"}
	if err := resolveAndInstallBasePlugin(t.Context(), dp, spec); err != nil {
		t.Fatalf("resolveAndInstallBasePlugin: %v", err)
	}

	active, err := data.NewPluginRepo(dp.Db).PluginActive(t.Context(), "ut-plugin-language-de")
	if err != nil {
		t.Fatalf("PluginActive: %v", err)
	}
	if !active {
		t.Fatal("expected ut-plugin-language-de to be installed and active")
	}
	if hits := mkt.downloadTokenHits(); hits != 1 {
		t.Fatalf("expected exactly one download-token request, got %d", hits)
	}
}

// If more than one catalog entry matches CanonicalType+Locale, the higher
// semver version must win — two different listings here (as a real catalog
// might briefly show during a staged rollout), not two versions of the same
// listing, so the assertion also proves the CORRECT listing got installed,
// not just the correct version.
func TestResolveAndInstallBasePlugin_PicksHighestSemverVersion(t *testing.T) {
	dp := newBasePluginTestDeps(t)
	mkt := newFakeMarketplace(t, map[string]string{
		"listing-lang-de-old": "ut-plugin-language-de-old",
		"listing-lang-de-new": "ut-plugin-language-de-new",
	})
	mkt.publishVersion(t, "listing-lang-de-old", "ut-plugin-language-de-old", "1.0.0")
	mkt.publishVersion(t, "listing-lang-de-new", "ut-plugin-language-de-new", "1.2.0")
	mkt.setCatalog(
		deLanguageCatalogEntry("listing-lang-de-old", "ut-plugin-language-de-old", "1.0.0"),
		deLanguageCatalogEntry("listing-lang-de-new", "ut-plugin-language-de-new", "1.2.0"),
	)
	dp.Cfg.Marketplace = mkt.config()

	spec := basePluginSpec{CanonicalType: "language", Locale: "de"}
	if err := resolveAndInstallBasePlugin(t.Context(), dp, spec); err != nil {
		t.Fatalf("resolveAndInstallBasePlugin: %v", err)
	}

	if active, _ := data.NewPluginRepo(dp.Db).PluginActive(t.Context(), "ut-plugin-language-de-new"); !active {
		t.Fatal("expected the higher-semver listing (1.2.0) to be installed")
	}
	if active, _ := data.NewPluginRepo(dp.Db).PluginActive(t.Context(), "ut-plugin-language-de-old"); active {
		t.Fatal("the lower-semver listing (1.0.0) must not have been installed")
	}
}

// The resolve step must not trust the server-side locale/type filter alone
// (ut-docs#591 design): a catalog response carrying entries that don't
// actually match CanonicalType+Locale must be filtered out client-side.
func TestResolveAndInstallBasePlugin_FiltersNonMatchingEntriesClientSide(t *testing.T) {
	dp := newBasePluginTestDeps(t)
	mkt := newFakeMarketplace(t, map[string]string{
		"listing-lang-fr": "ut-plugin-language-fr",
		"listing-theme":   "ut-plugin-theme-de",
		"listing-lang-de": "ut-plugin-language-de",
	})
	mkt.setCatalog(
		marketplace.PluginSummary{ID: "ut-plugin-language-fr", ListingID: "listing-lang-fr", Version: "1.0.0", CanonicalType: "language", AvailableLocales: []string{"fr"}},
		marketplace.PluginSummary{ID: "ut-plugin-theme-de", ListingID: "listing-theme", Version: "1.0.0", CanonicalType: "theme", AvailableLocales: []string{"de"}},
		deLanguageCatalogEntry("listing-lang-de", "ut-plugin-language-de", "1.0.0"),
	)
	dp.Cfg.Marketplace = mkt.config()

	spec := basePluginSpec{CanonicalType: "language", Locale: "de"}
	if err := resolveAndInstallBasePlugin(t.Context(), dp, spec); err != nil {
		t.Fatalf("resolveAndInstallBasePlugin: %v", err)
	}

	repo := data.NewPluginRepo(dp.Db)
	if active, _ := repo.PluginActive(t.Context(), "ut-plugin-language-de"); !active {
		t.Fatal("expected the matching language/de listing to be installed")
	}
	if active, _ := repo.PluginActive(t.Context(), "ut-plugin-language-fr"); active {
		t.Fatal("a language/fr listing must not be installed for a de spec")
	}
	if active, _ := repo.PluginActive(t.Context(), "ut-plugin-theme-de"); active {
		t.Fatal("a theme/de listing must not be installed for a language spec")
	}
}

// A listing published with a REGION-SUFFIXED locale tag ("de-DE") must still
// satisfy a bare "de" spec: ut-cloud's own catalog.localeAvailable compares
// primary language subtags, availableLocales comes straight from a plugin
// author's manifest (so the tag's shape is their choice, not a contract), and
// the POS already treats "de-DE" as "de" everywhere else it resolves a locale.
// Matching whole tags only would silently install nothing — #1055 all over
// again, one subtag further down.
func TestResolveAndInstallBasePlugin_MatchesRegionSuffixedCatalogLocale(t *testing.T) {
	dp := newBasePluginTestDeps(t)
	mkt := newFakeMarketplace(t, map[string]string{"listing-lang-de": "ut-plugin-language-de"})
	mkt.setCatalog(marketplace.PluginSummary{
		ID: "ut-plugin-language-de", ListingID: "listing-lang-de", Name: "German language pack",
		Version: "1.0.0", CanonicalType: "language", AvailableLocales: []string{"de-DE"},
	})
	dp.Cfg.Marketplace = mkt.config()

	spec := basePluginSpec{CanonicalType: "language", Locale: "de"}
	if err := resolveAndInstallBasePlugin(t.Context(), dp, spec); err != nil {
		t.Fatalf("resolveAndInstallBasePlugin: %v", err)
	}
	if active, _ := data.NewPluginRepo(dp.Db).PluginActive(t.Context(), "ut-plugin-language-de"); !active {
		t.Fatal(`expected a listing published as "de-DE" to satisfy the bare "de" spec`)
	}
}

// The flip side of the base-language rule: it must not become a catch-all.
// ut-cloud's localeAvailable deliberately treats an EMPTY availableLocales
// list (and an "en" entry) as matching every locale — right for browsing the
// catalog, wrong for deciding plugin identity, because it would auto-install
// the wrong language pack. A base plugin has to positively declare its locale.
func TestResolveAndInstallBasePlugin_UnrestrictedOrForeignListingNeverMatches(t *testing.T) {
	dp := newBasePluginTestDeps(t)
	mkt := newFakeMarketplace(t, map[string]string{
		"listing-lang-global": "ut-plugin-language-global",
		"listing-lang-en":     "ut-plugin-language-en",
	})
	mkt.setCatalog(
		// No availableLocales at all — "global" by ut-cloud's browse rule.
		marketplace.PluginSummary{ID: "ut-plugin-language-global", ListingID: "listing-lang-global", Version: "1.0.0", CanonicalType: "language"},
		// English pack: ut-cloud's browse rule says "en" is readable by all.
		marketplace.PluginSummary{ID: "ut-plugin-language-en", ListingID: "listing-lang-en", Version: "1.0.0", CanonicalType: "language", AvailableLocales: []string{"en-US"}},
	)
	dp.Cfg.Marketplace = mkt.config()

	spec := basePluginSpec{CanonicalType: "language", Locale: "de"}
	if err := resolveAndInstallBasePlugin(t.Context(), dp, spec); err != nil {
		t.Fatalf("expected a clean no-op, got %v", err)
	}
	repo := data.NewPluginRepo(dp.Db)
	if active, _ := repo.PluginActive(t.Context(), "ut-plugin-language-global"); active {
		t.Fatal("a listing declaring no locale must not be auto-installed for a de spec")
	}
	if active, _ := repo.PluginActive(t.Context(), "ut-plugin-language-en"); active {
		t.Fatal("an en-US language pack must not be auto-installed for a de spec")
	}
	if hits := mkt.downloadTokenHits(); hits != 0 {
		t.Fatalf("expected no download-token request, got %d", hits)
	}
}

// A country with nothing published yet for its locale is a no-op, not an
// error — the till just has nothing to install.
func TestResolveAndInstallBasePlugin_NoMatchingListingIsNoOp(t *testing.T) {
	dp := newBasePluginTestDeps(t)
	mkt := newFakeMarketplace(t, nil)
	mkt.setCatalog() // nothing published
	dp.Cfg.Marketplace = mkt.config()

	spec := basePluginSpec{CanonicalType: "language", Locale: "de"}
	if err := resolveAndInstallBasePlugin(t.Context(), dp, spec); err != nil {
		t.Fatalf("expected no error when nothing matches, got %v", err)
	}
	if hits := mkt.downloadTokenHits(); hits != 0 {
		t.Fatalf("expected no download-token request, got %d", hits)
	}
}

// Idempotency: calling resolveAndInstallBasePlugin again once the plugin is
// already active must be a clean no-op — no second download, no duplicate
// row, whether it's a second wizard run or the background retry re-running
// after a prior success.
//
// Wire-faithful (ut-docs#1063): the catalog entry sets ID == ListingID, as
// the real ut-cloud wire always does (there is no separate manifest-plugin-id
// field on PluginSummary) — unlike deLanguageCatalogEntry's helper shape,
// which models ID and ListingID as distinct values and would let a
// listing-UUID-keyed idempotency bug pass unnoticed, exactly as it did before
// ut-docs#1063's fix (the old PluginActive(best.ID) check could never match a
// `plugins` row keyed by manifest plugin id, so this test's own second call
// would previously have re-installed instead of no-op'ing).
func TestResolveAndInstallBasePlugin_IdempotentWhenAlreadyActive(t *testing.T) {
	dp := newBasePluginTestDeps(t)
	mkt := newFakeMarketplace(t, map[string]string{"listing-lang-de": "ut-plugin-language-de"})
	mkt.setCatalog(marketplace.PluginSummary{
		ID: "listing-lang-de", ListingID: "listing-lang-de", Name: "German language pack",
		Version: "1.0.0", CanonicalType: "language", AvailableLocales: []string{"de"},
	})
	dp.Cfg.Marketplace = mkt.config()

	spec := basePluginSpec{CanonicalType: "language", Locale: "de"}
	if err := resolveAndInstallBasePlugin(t.Context(), dp, spec); err != nil {
		t.Fatalf("first attempt: %v", err)
	}
	if err := resolveAndInstallBasePlugin(t.Context(), dp, spec); err != nil {
		t.Fatalf("second attempt: %v", err)
	}

	if hits := mkt.downloadTokenHits(); hits != 1 {
		t.Fatalf("expected exactly one download-token request across both attempts, got %d", hits)
	}
	var count int
	if err := dp.Db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM plugins WHERE id = 'ut-plugin-language-de'`).Scan(&count); err != nil {
		t.Fatalf("count plugins: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly one plugins row, got %d", count)
	}
}

// --- installBasePluginsForSetup (the wizard's own hook) ---

func TestInstallBasePluginsForSetup_NoMappingIsNoOp(t *testing.T) {
	dp := newBasePluginTestDeps(t)
	// No Marketplace endpoint configured at all — if this touched the
	// network it would error; the point of this test is that it must not
	// even try for a country with nothing in setupBasePlugins.
	installBasePluginsForSetup(t.Context(), dp, "US")

	if _, ok, _ := dp.Settings.Get(t.Context(), common.KeyPendingBasePlugins); ok {
		t.Fatal("expected no pending base plugins for an unmapped country")
	}
}

// The offline path: the catalog is unreachable at wizard-completion time, so
// installBasePluginsForSetup must still return promptly (never block) and
// leave the spec pending; a later background retry tick against a now-
// reachable marketplace must install it and clear the pending entry.
func TestInstallBasePluginsForSetup_OfflineThenBackgroundRetryInstalls(t *testing.T) {
	dp := newBasePluginTestDeps(t)

	// A closed local server: connections fail immediately (refused), so this
	// test doesn't pay setupBasePluginAttemptTimeout's full 5s waiting on a
	// real timeout — it's still exercising a genuine "catalog unreachable"
	// failure, not a mocked-out error.
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	dp.Cfg.Marketplace.EndpointURL = dead.URL
	dp.Cfg.Marketplace.ClientID = "merchant-1"
	dp.Cfg.Marketplace.StoreID = "store-1"
	dead.Close()

	installBasePluginsForSetup(t.Context(), dp, "DE")

	pending, err := loadPendingBasePlugins(t.Context(), dp)
	if err != nil {
		t.Fatalf("loadPendingBasePlugins: %v", err)
	}
	if len(pending) != 1 || pending[0] != (basePluginSpec{CanonicalType: "language", Locale: "de"}) {
		t.Fatalf("expected the DE language spec still pending, got %+v", pending)
	}
	if active, _ := data.NewPluginRepo(dp.Db).PluginActive(t.Context(), "ut-plugin-language-de"); active {
		t.Fatal("nothing should be installed while offline")
	}

	// Network returns: a real marketplace now serves the DE listing.
	mkt := newFakeMarketplace(t, map[string]string{"listing-lang-de": "ut-plugin-language-de"})
	mkt.setCatalog(deLanguageCatalogEntry("listing-lang-de", "ut-plugin-language-de", "1.0.0"))
	dp.Cfg.Marketplace = mkt.config()

	basePluginRetryTick(t.Context(), dp)

	pending, err = loadPendingBasePlugins(t.Context(), dp)
	if err != nil {
		t.Fatalf("loadPendingBasePlugins after retry: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected the pending list cleared after a successful retry, got %+v", pending)
	}
	if active, _ := data.NewPluginRepo(dp.Db).PluginActive(t.Context(), "ut-plugin-language-de"); !active {
		t.Fatal("expected the DE language plugin installed after the retry tick")
	}
}

// --- POST /api/setup end to end ---

func TestSetupWizardDE_HappyPathInstallsBasePluginSynchronously(t *testing.T) {
	mux, dp := newRealDBDeps(t)
	initTestPaths(t)
	mkt := newFakeMarketplace(t, map[string]string{"listing-lang-de": "ut-plugin-language-de"})
	mkt.setCatalog(deLanguageCatalogEntry("listing-lang-de", "ut-plugin-language-de", "1.0.0"))
	dp.Cfg.Marketplace = mkt.config()

	rec := postForm(mux, "/api/setup", url.Values{
		"pin":         {"2468"},
		"pin_confirm": {"2468"},
		"country":     {"DE"},
		"currency":    {"EUR"},
		"store_name":  {"Ecke Laden"},
	}, nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("wizard setup: code=%d body=%s", rec.Code, rec.Body.String())
	}

	if active, err := data.NewPluginRepo(dp.Db).PluginActive(t.Context(), "ut-plugin-language-de"); err != nil || !active {
		t.Fatalf("expected the German language pack installed synchronously: active=%v err=%v", active, err)
	}
	if pending, _ := loadPendingBasePlugins(t.Context(), dp); len(pending) != 0 {
		t.Fatalf("expected nothing left pending after a successful synchronous install, got %+v", pending)
	}
}

func TestSetupWizardDE_OfflineCompletesAndLeavesPendingForRetry(t *testing.T) {
	mux, dp := newRealDBDeps(t)
	initTestPaths(t)
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	dp.Cfg.Marketplace.EndpointURL = dead.URL
	dp.Cfg.Marketplace.ClientID = "merchant-1"
	dp.Cfg.Marketplace.StoreID = "store-1"
	dead.Close()

	rec := postForm(mux, "/api/setup", url.Values{
		"pin":         {"2468"},
		"pin_confirm": {"2468"},
		"country":     {"DE"},
		"currency":    {"EUR"},
		"store_name":  {"Ecke Laden"},
	}, nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("wizard setup must complete even offline: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if sessionCookie(rec) == "" {
		t.Fatal("wizard setup must still sign the admin in even when the base-plugin install is offline")
	}

	pending, err := loadPendingBasePlugins(t.Context(), dp)
	if err != nil {
		t.Fatalf("loadPendingBasePlugins: %v", err)
	}
	if len(pending) != 1 || pending[0].CanonicalType != "language" || pending[0].Locale != "de" {
		t.Fatalf("expected the DE language spec left pending, got %+v", pending)
	}
}

// --- StartBasePluginRetry lifecycle ---

// The background worker is registered against app.Run's drain WaitGroup
// (internal/pages/init.go), so it must return on ctx.Done() rather than
// leaking its goroutine — including while it is still inside the
// basePluginRetryInitialDelay window, which is far longer than a shutdown
// is willing to wait. Without the ctx.Done() arm on that first select, a
// till would sit on the delay before it could finish shutting down.
func TestStartBasePluginRetryShutsDownOnCtxDone(t *testing.T) {
	dp := newBasePluginTestDeps(t)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup

	StartBasePluginRetry(ctx, dp, &wg)
	cancel() // cancel while the worker is still in its initial delay

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("StartBasePluginRetry did not return on ctx.Done() — goroutine leak")
	}
}

// --- Settings page: pending base-plugin chip + dismiss ---

// The Settings → Data chip only shows once something is actually pending —
// not on a till with nothing (or nothing left) to install.
func TestSettingsShowsPendingBasePluginChipOnlyWhenPending(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

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

	if strings.Contains(getSettings(), `data-testid="pending-base-plugin"`) {
		t.Fatal("pending base-plugin chip shown with nothing pending")
	}

	if err := savePendingBasePlugins(t.Context(), d, []basePluginSpec{{CanonicalType: "language", Locale: "de"}}); err != nil {
		t.Fatal(err)
	}
	body := getSettings()
	if !strings.Contains(body, `data-testid="pending-base-plugin"`) {
		t.Fatal("pending base-plugin chip not shown once something is pending")
	}
	if !strings.Contains(body, "DE") {
		t.Error("chip does not mention the pending locale")
	}
}

// Dismissing a pending base-plugin entry is manager-gated (like the restore
// prompt) and removes just that entry, never installing it.
func TestSettingsDismissPendingBasePluginEndpoint(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	if err := savePendingBasePlugins(t.Context(), d, []basePluginSpec{
		{CanonicalType: "language", Locale: "de"},
		{CanonicalType: "language", Locale: "es"},
	}); err != nil {
		t.Fatal(err)
	}

	form := url.Values{"canonical_type": {"language"}, "locale": {"de"}}
	// ut-docs#865: elevation prompt, not a flat 403 (same as dismiss-restore-prompt).
	rec := postForm(mux, "/api/settings/dismiss-pending-base-plugin", form, &cashUser)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "elevation-dialog") {
		t.Fatalf("cashier dismiss: code=%d body=%s, want 200 with the elevation prompt", rec.Code, rec.Body.String())
	}
	if pending, _ := loadPendingBasePlugins(t.Context(), d); len(pending) != 2 {
		t.Fatalf("cashier attempt changed the pending list: %+v", pending)
	}

	rec = postForm(mux, "/api/settings/dismiss-pending-base-plugin", form, &mgrUser)
	// ut-docs#865 review finding F9: plain-allowed path stays a bare empty
	// body, no X-UT-Response — same reasoning as
	// TestSettingsDismissRestorePromptEndpoint's identical assertion.
	if rec.Header().Get("X-UT-Response") != "" {
		t.Fatalf("manager dismiss set X-UT-Response = %q, want unset on the plain-allowed path", rec.Header().Get("X-UT-Response"))
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("manager dismiss: code=%d body=%s", rec.Code, rec.Body.String())
	}
	pending, err := loadPendingBasePlugins(t.Context(), d)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Locale != "es" {
		t.Fatalf("expected only the es entry left, got %+v", pending)
	}
}

// ut-docs#868: canonical_type/locale must match a currently-pending spec
// BEFORE checkOrElevate runs. A mismatched pair is rejected 400, even for a
// manager (already-authorized session) — no PIN should ever be asked for a
// request that was always going to be a no-op, and the pending list must be
// left untouched. Covers both an unrelated valid-looking value and one
// containing the `"` that would otherwise break the retry's
// `[id="pending-plugin-msg-%s"]` CSS attribute selector / pollute the audit
// entity_id (the review finding this card fixes).
func TestSettingsDismissPendingBasePluginEndpoint_RejectsMismatch(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	if err := savePendingBasePlugins(t.Context(), d, []basePluginSpec{
		{CanonicalType: "language", Locale: "de"},
	}); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name          string
		canonicalType string
		locale        string
	}{
		{"unrelated value", "language", "fr"},
		{"selector-breaking value", `language"]<script`, "de"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			form := url.Values{"canonical_type": {tc.canonicalType}, "locale": {tc.locale}}
			// Manager: no PIN in play at all, so a bare 400 (not the
			// elevation prompt) proves validation ran before checkOrElevate.
			rec := postForm(mux, "/api/settings/dismiss-pending-base-plugin", form, &mgrUser)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("manager dismiss with mismatched pair: code=%d body=%s, want 400", rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "elevation-dialog") {
				t.Fatalf("manager dismiss with mismatched pair showed the elevation prompt, want a plain 400 before any gate check: %s", rec.Body.String())
			}
			pending, err := loadPendingBasePlugins(t.Context(), d)
			if err != nil {
				t.Fatal(err)
			}
			if len(pending) != 1 || pending[0].CanonicalType != "language" || pending[0].Locale != "de" {
				t.Fatalf("mismatched dismiss attempt changed the pending list: %+v", pending)
			}
		})
	}
}
