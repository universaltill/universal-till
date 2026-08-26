package pages

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
	"github.com/universaltill/universal-till/internal/plugins/oauth"
	"github.com/universaltill/universal-till/internal/updates"
)

// basePluginSpec identifies a free base plugin to auto-install once a
// merchant confirms their country in the setup wizard. ONLY canonical
// type "language" belongs in setupBasePlugins — a fiscal/tax entry would
// contradict ADR-0025 decision 4 (fiscal plugins are prompted, never
// silently auto-installed) and needs a superseding ADR first.
type basePluginSpec struct {
	CanonicalType string // ADR-0002 canonical plugin type
	Locale        string // catalog locale filter, e.g. "de"
}

// setupBasePlugins is the country → free-base-plugins table (ut-docs#591):
// declared data, reviewable in a PR diff, extendable by adding a row — no
// core code change. Every shop gets its country's entries, subscribed or
// not (ut-docs/architecture/monetization-cloud-services.md already excludes
// locally-installed free plugins/language packs from anything paid).
var setupBasePlugins = map[string][]basePluginSpec{
	"DE": {{CanonicalType: "language", Locale: "de"}},
	"ES": {{CanonicalType: "language", Locale: "es"}},
}

// setupBasePluginAttemptTimeout bounds the ONE synchronous resolve+install
// attempt POST /api/setup makes before it responds — offline-first means
// setup itself must never block on a download (ut-docs#591 AC).
const setupBasePluginAttemptTimeout = 5 * time.Second

// basePluginRetryInitialDelay/basePluginRetryInterval shape the background
// retry loop, mirroring internal/updates.Start's "short delay, then a
// ticker" idiom. Unlike sync_admin.go's brokenRefetchMaxAttempts/
// brokenRefetchBackoffTicks (a plugin that may genuinely never load again,
// so retries taper off and eventually go rare), a base-plugin install that's
// merely offline has no terminal failure to give up on — it only needs the
// network back — so this retries indefinitely rather than capping attempts.
// The interval is deliberately far looser than the 30s plugin-sync tick
// (not hammering the marketplace over an install that isn't urgent), but
// still frequent enough that a shop back online converges within the same
// working session.
const (
	basePluginRetryInitialDelay = 30 * time.Second
	basePluginRetryInterval     = 5 * time.Minute
)

// setupWizardLanguageInstallTimeout bounds POST /api/setup/language's
// foreground install (ut-docs#1092). Deliberately a SEPARATE constant from
// setupBasePluginAttemptTimeout: that one caps a silent, background,
// best-effort attempt the operator never waits on, while this is a
// user-initiated install the operator is actively watching — it gets room
// for a real download before degrading to the background retry.
const setupWizardLanguageInstallTimeout = 20 * time.Second

// setupLanguageCatalogTTL / setupLanguageCatalogFetchTimeout shape the
// wizard step-1 language-tile catalog cache (ut-docs#1092): the fetch is
// time-boxed well under the page render's patience so GET /setup can never
// hang on the catalog, and a successful response is reused for the TTL so
// stepping back and forth through the wizard doesn't hammer the marketplace.
const (
	setupLanguageCatalogTTL          = 5 * time.Minute
	setupLanguageCatalogFetchTimeout = 4 * time.Second
)

// setupLanguageCatalog is the package-level TTL cache behind
// setupCatalogLanguageLocales. Process-global on purpose (the catalog is the
// same for every request) and mutex-guarded; `locales` holds normalized
// base-language tags, sorted, so renders are stable.
var setupLanguageCatalog struct {
	mu        sync.Mutex
	fetchedAt time.Time
	locales   []string
	hasData   bool
}

// setupCatalogLanguageLocales returns every base-language tag some
// marketplace canonical_type=language listing declares it serves — the
// wizard's "languages you could have" browse list (ut-docs#1092). This is
// deliberately looser than resolveAndInstallBasePlugin's identity match
// (browsing wants everything, with no Locale filter on the request), but
// still filters CanonicalType client-side — the server-side capability
// filter isn't trusted alone, same posture as the resolve step above.
//
// Freshness: a fetch newer than setupLanguageCatalogTTL is served straight
// from the cache; on a fetch error the stale cache (if any) is served rather
// than dropping the tiles, and with nothing cached at all it reports
// ok=false so the caller can degrade to bundled-only plus an honest
// "more once connected" note. Never returns an error: offline-first means
// the wizard render must not care why the catalog is missing.
//
// Uses enroll.Effective (not EnsureRegistered): catalog browsing needs no
// store identity — same as plugins_store_page.go's storeInstaller — and a
// lazy registration attempt on every first-boot render would be pure delay.
func setupCatalogLanguageLocales(ctx context.Context, d *common.Deps) ([]string, bool) {
	setupLanguageCatalog.mu.Lock()
	if setupLanguageCatalog.hasData && time.Since(setupLanguageCatalog.fetchedAt) < setupLanguageCatalogTTL {
		cached := append([]string(nil), setupLanguageCatalog.locales...)
		setupLanguageCatalog.mu.Unlock()
		return cached, true
	}
	setupLanguageCatalog.mu.Unlock()

	// Nil-safe on Cfg (some auth-page test/tool deps run without one):
	// no config means no catalog to ask, the same degrade as unreachable.
	if d == nil || d.Cfg == nil {
		return nil, false
	}

	fetchCtx, cancel := context.WithTimeout(ctx, setupLanguageCatalogFetchTimeout)
	defer cancel()
	effCfg := enroll.Effective(d.Cfg)
	client := marketplace.NewClient(&effCfg.Marketplace, oauth.NewTokenClient(&effCfg.Marketplace))
	resp, err := client.ListPlugins(fetchCtx, &marketplace.ListPluginsRequest{Capability: []string{"language"}})
	if err != nil {
		logging.L().Infof("setup wizard: language catalog unreachable: %v", err)
		setupLanguageCatalog.mu.Lock()
		defer setupLanguageCatalog.mu.Unlock()
		if setupLanguageCatalog.hasData {
			return append([]string(nil), setupLanguageCatalog.locales...), true
		}
		return nil, false
	}

	seen := map[string]bool{}
	locales := []string{}
	for i := range resp.Plugins {
		p := &resp.Plugins[i]
		if p.CanonicalType != "language" {
			continue
		}
		for _, l := range p.AvailableLocales {
			b := baseLang(strings.TrimSpace(l))
			// The catalog is an UNSIGNED marketplace response — Ed25519
			// only verifies the plugin artifact itself, never this listing
			// metadata — and these values are cached and later rendered
			// straight into Alpine JS-attribute expressions
			// (setup.html's @click/:aria-busy). isBareLocaleCode is the
			// same gate setup_page.go already applies to every other
			// externally-influenced locale value; anything that fails it
			// here must never reach the cache, let alone the template.
			if !isBareLocaleCode(b) || seen[b] {
				continue
			}
			seen[b] = true
			locales = append(locales, b)
		}
	}
	sort.Strings(locales)

	setupLanguageCatalog.mu.Lock()
	setupLanguageCatalog.locales = locales
	setupLanguageCatalog.fetchedAt = time.Now()
	setupLanguageCatalog.hasData = true
	setupLanguageCatalog.mu.Unlock()
	return append([]string(nil), locales...), true
}

// setupCatalogOnlyLanguages drops every catalog locale the till already
// serves (base-language comparison, both directions region-agnostic — an
// installed "de" covers a catalog "de-DE" and vice versa): those already get
// a bundled/installed tile via {{ range locales }}, so a second tile would
// just be the same language twice.
func setupCatalogOnlyLanguages(catalog, available []string) []string {
	out := []string{}
	for _, c := range catalog {
		covered := false
		for _, a := range available {
			if baseLang(strings.TrimSpace(a)) == baseLang(strings.TrimSpace(c)) {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, c)
		}
	}
	return out
}

// addPendingBasePlugin merges ONE spec into the persisted pending list
// (append-if-absent), unlike savePendingBasePlugins' wholesale replace —
// the wizard's language step must join #1055's background retry without
// clobbering whatever the country step already queued. An unreadable list
// (corrupt JSON) is replaced rather than kept: persisting this spec matters
// more than preserving bytes no reader can parse anyway.
func addPendingBasePlugin(ctx context.Context, d *common.Deps, spec basePluginSpec) error {
	pending, err := loadPendingBasePlugins(ctx, d)
	if err != nil {
		logging.L().Warnf("setup wizard: pending base plugins unreadable, resetting list: %v", err)
		pending = nil
	}
	for _, s := range pending {
		if s == spec {
			return nil
		}
	}
	return savePendingBasePlugins(ctx, d, append(pending, spec))
}

// installBasePluginsForSetup is POST /api/setup's hook (ut-docs#591): looks
// up the confirmed country's free base plugins, persists them as pending
// BEFORE any network attempt (so the list survives even if the process dies
// mid-request), then makes one best-effort, time-boxed synchronous attempt
// to resolve+install right here. Mirrors this same handler's other
// best-effort steps (restore-choice, demo-data seed) — a failure here must
// never delay or fail the wizard's own response, so every error is logged
// and swallowed.
func installBasePluginsForSetup(ctx context.Context, d *common.Deps, country string) {
	specs := setupBasePlugins[strings.ToUpper(strings.TrimSpace(country))]
	if len(specs) == 0 {
		return
	}
	pending := append([]basePluginSpec(nil), specs...)
	// Merge (append-if-absent), never wholesale-replace: ut-docs#1092's
	// wizard language step can already have queued an unrelated spec (e.g.
	// a catalog-only language picked at step 1, still offline) before the
	// country step ever runs. A plain savePendingBasePlugins here silently
	// dropped that spec — found by independent review, TestInstallBase-
	// PluginsForSetup_MergesWithLanguageStepPending pins the fix.
	for _, spec := range pending {
		if err := addPendingBasePlugin(ctx, d, spec); err != nil {
			logging.L().Errorf("setup wizard: persist pending base plugins: %v", err)
		}
	}

	attemptCtx, cancel := context.WithTimeout(ctx, setupBasePluginAttemptTimeout)
	defer cancel()
	for _, spec := range pending {
		if err := resolveAndInstallBasePlugin(attemptCtx, d, spec); err != nil {
			logging.L().Warnf("setup wizard: base plugin %s/%s not installed yet, will retry in background: %v",
				spec.CanonicalType, spec.Locale, err)
			continue
		}
		// Installed (or a no-op because an equivalent plugin was already
		// active): drop just THIS spec from whatever else is pending —
		// same reasoning as above, a wholesale replace here would erase an
		// unrelated spec another step queued.
		if err := dismissPendingBasePlugin(ctx, d, spec.CanonicalType, spec.Locale); err != nil {
			logging.L().Errorf("setup wizard: clear installed pending base plugin %s/%s: %v",
				spec.CanonicalType, spec.Locale, err)
		}
	}
}

// resolveAndInstallBasePlugin resolves spec against the marketplace catalog
// and installs the highest-semver matching listing through the existing
// Ed25519-verified install path (cloudInstallPluginVersion) — never a second
// install code path. Filtering is done client-side on BOTH CanonicalType and
// locale even though the request also asks the server to filter by locale:
// the server-side filter isn't reliably applied, so this never trusts it
// alone. The locale test is "spec.Locale is among the listing's
// AvailableLocales" (the real catalog's per-listing availableLocales array —
// a listing can serve several locales; #1055: matching on a singular field
// the real server never sent is what made this silently install nothing).
// Returns nil when there's genuinely nothing to do (no listing
// published yet, or an equivalent plugin is already active — idempotent on
// a retry or a second wizard run) or a non-nil error describing why the
// spec should stay pending for the next attempt.
func resolveAndInstallBasePlugin(ctx context.Context, d *common.Deps, spec basePluginSpec) error {
	effCfg := enroll.EnsureRegistered(ctx, d.Cfg, d.Settings)
	client := marketplace.NewClient(&effCfg.Marketplace, oauth.NewTokenClient(&effCfg.Marketplace))
	resp, err := client.ListPlugins(ctx, &marketplace.ListPluginsRequest{Locale: spec.Locale})
	if err != nil {
		return fmt.Errorf("catalog unreachable: %w", err)
	}

	var best *marketplace.PluginSummary
	for i := range resp.Plugins {
		p := &resp.Plugins[i]
		if p.CanonicalType != spec.CanonicalType || !localeInList(p.AvailableLocales, spec.Locale) {
			continue
		}
		if best == nil || updates.Newer(p.Version, best.Version) {
			best = p
		}
	}
	if best == nil {
		// Nothing published for this country/locale yet — not an error, just
		// nothing to install (mirrors the "country with nothing mapped"
		// no-op, one level down).
		return nil
	}

	pluginID := best.ID
	if pluginID == "" {
		pluginID = best.ListingID
	}
	if active, activeErr := data.NewPluginRepo(d.Db).PluginActive(ctx, pluginID); activeErr == nil && active {
		return nil // idempotent: an equivalent plugin is already installed and active
	}

	listingID := best.ListingID
	if listingID == "" {
		listingID = best.ID
	}
	if _, err := cloudInstallPluginVersion(ctx, d, listingID, best.Version); err != nil {
		return fmt.Errorf("install %s@%s: %w", listingID, best.Version, err)
	}
	return nil
}

// localeInList reports whether the catalog listing's availableLocales cover
// want. Two tags match when their base language matches, compared
// case-insensitively: locale tags are case-insensitive ("de" == "DE"), the
// catalog's casing is the server's choice rather than a contract, and a
// listing published as "de-DE" is still the German pack a `de` spec wants.
// That base-language rule is the same one the POS already applies to its own
// locale lookups (baseLang in plugin_page.go, config.baseLang's region-tag
// fallback) and mirrors ut-cloud's primaryLang comparison in
// catalog.localeAvailable — matching only whole tags would re-open #1055 for
// any pack published with a region subtag.
//
// It deliberately does NOT mirror the other two branches of ut-cloud's
// localeAvailable, which treat an EMPTY availableLocales list and an "en"
// entry as matching every requested locale. Those are right for *browsing*
// the catalog (show the merchant everything they could read) and wrong here,
// where the match decides plugin *identity*: an unrestricted or English
// listing satisfying a `de` spec would silently auto-install the wrong
// language pack. A base plugin must positively declare the locale it serves.
func localeInList(locales []string, want string) bool {
	wantBase := baseLang(strings.TrimSpace(want))
	if wantBase == "" {
		return false
	}
	for _, l := range locales {
		if baseLang(strings.TrimSpace(l)) == wantBase {
			return true
		}
	}
	return false
}

// loadPendingBasePlugins / savePendingBasePlugins persist the still-pending
// specs as JSON under common.KeyPendingBasePlugins, so the list survives a
// process restart between the wizard's own attempt and the background
// retry — and so a merchant can dismiss an entry (Settings) by rewriting the
// list without either side needing to know about the other's in-memory state.
func loadPendingBasePlugins(ctx context.Context, d *common.Deps) ([]basePluginSpec, error) {
	raw, ok, err := d.Settings.Get(ctx, common.KeyPendingBasePlugins)
	if err != nil || !ok || strings.TrimSpace(raw) == "" {
		return nil, err
	}
	var specs []basePluginSpec
	if err := json.Unmarshal([]byte(raw), &specs); err != nil {
		return nil, err
	}
	return specs, nil
}

func savePendingBasePlugins(ctx context.Context, d *common.Deps, specs []basePluginSpec) error {
	if len(specs) == 0 {
		return d.Settings.Set(ctx, common.KeyPendingBasePlugins, "")
	}
	raw, err := json.Marshal(specs)
	if err != nil {
		return err
	}
	return d.Settings.Set(ctx, common.KeyPendingBasePlugins, string(raw))
}

// basePluginRetryTick is one pass of the background retry: read whatever is
// still pending, attempt each once more, and persist whatever's left.
// Silent-and-retry on failure, same posture as internal/updates' checkOnce —
// this never surfaces an error to a caller, only to the log and the
// Settings chip (via the persisted pending list itself).
func basePluginRetryTick(ctx context.Context, d *common.Deps) {
	pending, err := loadPendingBasePlugins(ctx, d)
	if err != nil {
		logging.L().Warnf("base plugin retry: load pending list: %v", err)
		return
	}
	if len(pending) == 0 {
		return
	}
	remaining := make([]basePluginSpec, 0, len(pending))
	for _, spec := range pending {
		if err := resolveAndInstallBasePlugin(ctx, d, spec); err != nil {
			logging.L().Infof("base plugin retry: %s/%s still pending: %v", spec.CanonicalType, spec.Locale, err)
			remaining = append(remaining, spec)
		}
	}
	if len(remaining) != len(pending) {
		if err := savePendingBasePlugins(ctx, d, remaining); err != nil {
			logging.L().Errorf("base plugin retry: persist pending list: %v", err)
		}
	}
}

// pendingBasePluginView is the Settings-page display shape for one still-
// pending basePluginSpec: CanonicalType/Locale round-trip verbatim to the
// dismiss endpoint (via jsonVals in the template), plus an upper-cased
// locale for the status line (e.g. "de" -> "DE").
type pendingBasePluginView struct {
	CanonicalType string
	Locale        string
	LocaleUpper   string
}

// pendingBasePluginViews maps the persisted specs to their Settings-page
// display shape. A merchant sees exactly what's still pending — "the
// merchant sees which one and why" (ut-docs#591 AC) — with a dismiss action
// per entry that removes just that one from common.KeyPendingBasePlugins
// (dismissPendingBasePlugin below), satisfying "a merchant can decline
// anything auto-installed" for the not-yet-installed case; once a plugin IS
// installed, the existing uninstall flow already covers removal.
func pendingBasePluginViews(specs []basePluginSpec) []pendingBasePluginView {
	views := make([]pendingBasePluginView, 0, len(specs))
	for _, s := range specs {
		views = append(views, pendingBasePluginView{
			CanonicalType: s.CanonicalType,
			Locale:        s.Locale,
			LocaleUpper:   strings.ToUpper(s.Locale),
		})
	}
	return views
}

// dismissPendingBasePlugin drops one spec (matched on CanonicalType+Locale)
// from the pending list without installing it — the settings page's dismiss
// button. A spec already gone (raced with a successful install, or a
// double-click) is a clean no-op, not an error.
func dismissPendingBasePlugin(ctx context.Context, d *common.Deps, canonicalType, locale string) error {
	pending, err := loadPendingBasePlugins(ctx, d)
	if err != nil {
		return err
	}
	remaining := make([]basePluginSpec, 0, len(pending))
	for _, s := range pending {
		if s.CanonicalType == canonicalType && s.Locale == locale {
			continue
		}
		remaining = append(remaining, s)
	}
	return savePendingBasePlugins(ctx, d, remaining)
}

// StartBasePluginRetry launches the background half of the country
// base-plugin auto-install (ut-docs#591): the wizard's own attempt only gets
// setupBasePluginAttemptTimeout, so anything still offline (or otherwise
// failing) at that point is retried here until it succeeds or the merchant
// dismisses it from Settings. Shape mirrors internal/updates.Start exactly —
// a goroutine, a short initial delay, then a ticker, everything
// silent-and-retry, wg.Done() on ctx.Done(). Wired in internal/pages/init.go
// alongside StartCloudSync/StartSyncPull/etc — NOT in internal/app/app.go,
// because (like those) it needs the *common.Deps pages.Init builds, which
// doesn't exist yet at the point app.go starts updates.Start/alerts.Start.
func StartBasePluginRetry(ctx context.Context, d *common.Deps, wg *sync.WaitGroup) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		select {
		case <-time.After(basePluginRetryInitialDelay):
		case <-ctx.Done():
			return
		}
		basePluginRetryTick(ctx, d)
		t := time.NewTicker(basePluginRetryInterval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				basePluginRetryTick(ctx, d)
			case <-ctx.Done():
				return
			}
		}
	}()
}
