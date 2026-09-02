package pages

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
	"github.com/universaltill/universal-till/internal/plugins/oauth"
	"github.com/universaltill/universal-till/internal/updates"
)

// ut-docs#1180: ADR-0025 decision 4 — a fiscal ("tax") plugin such as
// ut-plugin-tax-de (§12 UStG dine-in/takeaway VAT-rate-switching plus TSE
// signing and DSFinV-K export) must never be silently auto-installed —
// setupBasePlugins (setup_base_plugins.go) is explicitly documented
// language-packs-only for exactly this reason. Instead the wizard's country
// step PROMPTS: a tile appears only when the marketplace actually has a
// tax-type listing for the selected country, and it installs only on an
// explicit click. This mirrors setup_language_catalog.go's
// prompt-then-click-then-foreground-install-with-background-retry-fallback
// shape as closely as possible, reusing its helpers (resolveAndInstallBasePlugin,
// localeInList, loadPendingBasePlugins/savePendingBasePlugins) rather than a
// second install or retry mechanism.

// countryTaxLocale maps a wizard country code to the locale its fiscal
// plugin listing is expected to declare in AvailableLocales. Deliberately
// minimal — DE only, not every country ADR-0025 will eventually cover; an
// audit of which other countries need a fiscal plugin listing is an
// explicit non-goal here, tracked separately.
//
// NOTE when adding a country here: the tile itself lives in setup.html's
// step 3, which step 2's Next only routes to for DE
// (`country === 'DE' ? 3 : 4`), and renderWizard resumes a tax-plugin
// install round-trip on step 3 for the same reason. Adding e.g. "FR" to
// this map alone would resolve a listing that no operator can ever see —
// the step gating has to move too. As of ut-docs#1460, renderWizard also
// resolves this tile's data with the hardcoded tseProvisionCountry
// (setup_tse.go), not a country looked up from this map — that hardcode
// needs to move too, to whatever decides which of several tax-mapped
// countries' step the operator is actually in.
var countryTaxLocale = map[string]string{
	"DE": "de",
}

// setupWizardTaxInstallTimeout bounds POST /api/setup/tax-plugin's one
// synchronous resolve+install attempt. Same value and reasoning as
// setupWizardLanguageInstallTimeout (setup_language_catalog.go): this is a
// FOREGROUND action the operator explicitly chose and is watching a spinner
// for, so it gets long enough to actually download a fiscal plugin on shop
// WiFi before falling back to the same background retry.
const setupWizardTaxInstallTimeout = 20 * time.Second

const (
	// setupTaxCatalogTTL/FetchTimeout/RetryInterval/MaxPages mirror
	// setupLanguageCatalogTTL/FetchTimeout/RetryInterval/MaxPages
	// (setup_language_catalog.go) exactly, same values and reasoning — a
	// separate cache rather than sharing the language one because the two
	// browse different Capability filters ("tax" vs "language") and must
	// never leak entries into each other.
	setupTaxCatalogTTL           = 5 * time.Minute
	setupTaxCatalogFetchTimeout  = 3 * time.Second
	setupTaxCatalogRetryInterval = 30 * time.Second
	setupTaxCatalogMaxPages      = 25
)

// setupTaxCatalogCache is the package-level, mutex-guarded TTL cache behind
// the wizard's tax-plugin tile — same shape and posture as
// setupLangCatalogCache.
var setupTaxCatalogCache struct {
	mu          sync.Mutex
	lastAttempt time.Time
	lastSuccess time.Time
	fetched     bool
	entries     []marketplace.PluginSummary
}

// resetSetupTaxCatalog clears the cache — tests only (the cache is
// package-global, so without this one test's catalog leaks into every other
// render within the TTL).
func resetSetupTaxCatalog() {
	c := &setupTaxCatalogCache
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastAttempt = time.Time{}
	c.lastSuccess = time.Time{}
	c.fetched = false
	c.entries = nil
}

// setupTaxCatalogEntries returns the cached tax-capability catalog, fetching
// (bounded) when the cache is cold or expired. ok=false means the catalog is
// unreachable AND nothing was ever cached. Mirrors setupLanguageCatalogEntries
// field-for-field (same TTL/stale-cache/pagination/offline-first posture) —
// see that function's own doc comment for the full reasoning, not repeated
// here.
func setupTaxCatalogEntries(ctx context.Context, d *common.Deps) (entries []marketplace.PluginSummary, ok bool) {
	c := &setupTaxCatalogCache
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	if c.fetched && now.Sub(c.lastSuccess) < setupTaxCatalogTTL {
		return c.entries, true
	}
	if now.Sub(c.lastAttempt) < setupTaxCatalogRetryInterval {
		return c.entries, c.fetched
	}
	c.lastAttempt = now

	if d.Cfg == nil || strings.TrimSpace(d.Cfg.Marketplace.EndpointURL) == "" {
		return c.entries, c.fetched
	}

	fctx, cancel := context.WithTimeout(ctx, setupTaxCatalogFetchTimeout)
	defer cancel()
	// enroll.Effective, NOT enroll.EnsureRegistered — same ADR-0015 (lazy
	// store registration) reasoning as setupLanguageCatalogEntries: browsing
	// the catalog must never mint the shop's cloud store identity.
	effCfg := enroll.Effective(d.Cfg)
	client := marketplace.NewClient(&effCfg.Marketplace, oauth.NewTokenClient(&effCfg.Marketplace))
	var all []marketplace.PluginSummary
	pageToken := ""
	for page := 0; page < setupTaxCatalogMaxPages; page++ {
		resp, err := client.ListPlugins(fctx, &marketplace.ListPluginsRequest{Capability: []string{"tax"}, PageToken: pageToken})
		if err != nil {
			logging.L().Warnf("setup wizard: tax catalog fetch failed (serving %s): %v",
				map[bool]string{true: "stale cache", false: "nothing"}[c.fetched], err)
			return c.entries, c.fetched
		}
		all = append(all, resp.Plugins...)
		if resp.NextPageToken == "" {
			break
		}
		pageToken = resp.NextPageToken
	}
	c.entries = all
	c.fetched = true
	c.lastSuccess = now
	return c.entries, true
}

// installableTaxPlugin is the wizard's (at most one) tax-plugin tile: the
// country it's for, and the backing listing id (display/debug only — the
// install POST only needs country, re-resolving the listing server-side so
// the request body can't pick an arbitrary one).
type installableTaxPlugin struct {
	Country   string
	ListingID string
}

// setupInstallableTaxPlugin resolves the wizard's current country against
// countryTaxLocale, the cached tax-capability catalog, and the till's own
// install-status store, returning the single best (highest-semver) match
// that isn't already installed, or nil when there's genuinely nothing to
// prompt:
//   - no locale mapped for this country: nil, false — nothing to prompt,
//     not "catalog unavailable" (no note should show either).
//   - the catalog is unreachable with nothing cached: nil, true.
//   - no CanonicalType=="tax" listing declares the mapped locale: nil, false.
//   - the best match is already installed and active: nil, false — this is
//     what makes the tile disappear the moment install actually lands.
func setupInstallableTaxPlugin(ctx context.Context, d *common.Deps, country string) (plugin *installableTaxPlugin, catalogUnavailable bool) {
	locale, ok := countryTaxLocale[strings.ToUpper(strings.TrimSpace(country))]
	if !ok {
		return nil, false
	}

	entries, fetched := setupTaxCatalogEntries(ctx, d)
	if !fetched {
		return nil, true
	}

	// Filter to CanonicalType=="tax" via the EXISTING localeInList helper
	// (setup_base_plugins.go) — never a second locale-matching
	// implementation. Highest-semver wins, the same updates.Newer
	// comparison resolveAndInstallBasePlugin itself uses.
	var best *marketplace.PluginSummary
	for i := range entries {
		e := &entries[i]
		if e.CanonicalType != "tax" || !localeInList(e.AvailableLocales, locale) {
			continue
		}
		if best == nil || updates.Newer(e.Version, best.Version) {
			best = e
		}
	}
	if best == nil {
		return nil, false
	}

	listingID := best.ListingID
	if listingID == "" {
		listingID = best.ID
	}
	// Already installed+active: nothing to prompt. Same posture as
	// resolveAndInstallBasePlugin's own idempotency check — a store read
	// error is treated as "not installed" (fail open to still prompting,
	// never fail closed hiding a real install action).
	if status, hadStatus, statusErr := plugins.NewInstallStatusStore(d.Db).Get(ctx, listingID); statusErr == nil && hadStatus && status.State == plugins.InstallStateActive && status.PluginID != "" {
		return nil, false
	}

	return &installableTaxPlugin{Country: strings.ToUpper(strings.TrimSpace(country)), ListingID: listingID}, false
}

// setupTaxPluginInstallHandler is POST /api/setup/tax-plugin (ut-docs#1180):
// the wizard's explicit "install the fiscal plugin" action. Auth-exempt on
// the same first-boot-only window as POST /api/setup/language —
// NeedsFirstBoot is the gate. Mirrors setupLanguageInstallHandler closely:
// same server-side re-derivation of locale from the posted country (never
// trust a client-posted locale/listing), same foreground-install-then-
// pending-fallback shape, reusing resolveAndInstallBasePlugin/
// loadPendingBasePlugins/savePendingBasePlugins rather than a second install
// or retry mechanism.
func setupTaxPluginInstallHandler(d *common.Deps, svc *auth.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		firstBoot, err := svc.NeedsFirstBoot(r.Context())
		if err != nil || !firstBoot {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		_ = r.ParseForm()
		country := strings.ToUpper(strings.TrimSpace(r.PostFormValue("country")))
		locale, ok := countryTaxLocale[country]
		if !ok {
			// Genuinely forged — no country to resume the wizard on.
			http.Redirect(w, r, "/setup", http.StatusSeeOther)
			return
		}
		// Every redirect below carries the country back so renderWizard
		// resumes on step 3 with the operator's own pick intact, instead of
		// bouncing them to step 1 with the country re-detected from the OS.
		// This tile is not on step 1 like the language ones, so a bare
		// /setup would lose real work.
		resume := "/setup?tax_country=" + url.QueryEscape(country)

		// Re-check via the SAME resolution the real UI's tile is rendered
		// from that this country genuinely has an installable match right
		// now — the request body never gets to pick an arbitrary listing,
		// and a forged/stale POST (a country with no current catalog match,
		// or one already installed since the tile rendered) is rejected
		// clean, same posture as the language handler's `known` check.
		if match, _ := setupInstallableTaxPlugin(r.Context(), d, country); match == nil {
			http.Redirect(w, r, resume, http.StatusSeeOther)
			return
		}

		spec := basePluginSpec{CanonicalType: "tax", Locale: locale}
		ctx, cancel := context.WithTimeout(r.Context(), setupWizardTaxInstallTimeout)
		err = resolveAndInstallBasePlugin(ctx, d, spec)
		cancel()
		if err == nil {
			http.Redirect(w, r, resume, http.StatusSeeOther)
			return
		}
		logging.L().Warnf("setup wizard: foreground install of tax/%s failed, joining background retry: %v", locale, err)

		// Failure/timeout: join the EXISTING ut-docs#591 pending list — the
		// background retry (StartBasePluginRetry) and the Settings pending
		// chip both already act on it; no second retry mechanism. r.Context(),
		// not the (possibly expired) install ctx: the persist must still
		// work after a timeout.
		pending, loadErr := loadPendingBasePlugins(r.Context(), d)
		if loadErr != nil {
			logging.L().Errorf("setup wizard: load pending base plugins: %v", loadErr)
		}
		already := false
		for _, p := range pending {
			if p == spec {
				already = true
				break
			}
		}
		if !already {
			if saveErr := savePendingBasePlugins(r.Context(), d, append(pending, spec)); saveErr != nil {
				logging.L().Errorf("setup wizard: persist pending tax plugin install: %v", saveErr)
			}
		}

		http.Redirect(w, r, resume+"&tax_plugin_pending=1", http.StatusSeeOther)
	}
}
