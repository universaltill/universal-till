package pages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
	"github.com/universaltill/universal-till/internal/plugins/oauth"
	"github.com/universaltill/universal-till/internal/updates"
	"github.com/universaltill/universal-till/web/locales"
)

// basePluginSpec identifies a free base plugin to auto-install once a
// merchant confirms their country in the setup wizard. ONLY canonical
// type "language" belongs in basePluginsForCountry — a fiscal/tax entry would
// contradict ADR-0025 decision 4 (fiscal plugins are prompted, never
// silently auto-installed) and needs a superseding ADR first.
type basePluginSpec struct {
	CanonicalType string // ADR-0002 canonical plugin type
	Locale        string // catalog locale filter, e.g. "de"
}

// basePluginsForCountry is a country's free base plugins (ut-docs#591):
// the language pack for the language of the country's own DefaultLocale
// (country_settings data, ut-docs#1027), unless core already bundles that
// language in web/locales. It replaced a hard-coded country table
// (ut-docs#2964): core never names a country (ADR-0121 §11, guard-core-neutral),
// so a new market needs only its country row, never a code change here.
// DE→de and ES→es resolve exactly as the table did; PT→pt is new. Every
// shop gets its country's entries, subscribed or not
// (ut-docs/architecture/monetization-cloud-services.md already excludes
// locally-installed free plugins/language packs from anything paid). A
// language with no published pack is a silent no-op one level down
// (resolveAndInstallBasePlugin), so a country without one costs a single
// catalog query, never a stuck pending entry. A read error or a malformed
// operator-typed locale returns nil: setup never fails on this.
func basePluginsForCountry(ctx context.Context, d *common.Deps, country string) []basePluginSpec {
	if strings.TrimSpace(country) == "" {
		return nil
	}
	cs, ok, err := data.NewCountrySettingsRepo(d.Db).Get(ctx, country)
	if err != nil {
		logging.L().Warnf("base plugins: read country %q: %v", country, err)
		return nil
	}
	if !ok {
		return nil
	}
	lang := baseLang(strings.TrimSpace(cs.DefaultLocale))
	if !isLanguageSubtag(lang) || coreBundlesLanguage(lang) {
		return nil
	}
	return []basePluginSpec{{CanonicalType: "language", Locale: lang}}
}

// isLanguageSubtag reports whether s is a plain BCP-47 primary language
// subtag (2–3 ASCII letters), the only shape a catalog locale query or a
// pending spec may carry.
func isLanguageSubtag(s string) bool {
	if len(s) < 2 || len(s) > 3 {
		return false
	}
	for _, r := range s {
		if r < 'a' || r > 'z' {
			return false
		}
	}
	return true
}

// coreBundlesLanguage reports whether core ships lang's base locale file
// (web/locales/<lang>.json), in which case no pack is needed.
func coreBundlesLanguage(lang string) bool {
	_, err := fs.Stat(locales.FS, lang+".json")
	return err == nil
}

// setupBasePluginAttemptTimeout bounds the ONE synchronous resolve+install
// attempt POST /api/setup makes before it responds — offline-first means
// setup itself must never block on a download (ut-docs#591 AC).
const setupBasePluginAttemptTimeout = 5 * time.Second

// setupBasePluginMaxPages bounds how many pages resolveAndInstallBasePlugin
// will follow before giving up — mirrors setupLanguageCatalogMaxPages/
// setupTaxCatalogMaxPages (setup_language_catalog.go/setup_tax_catalog.go)
// exactly, same value and reasoning (ut-docs#2133): ut-cloud's real
// ListPlugins defaults to a 20-listing page (internal/catalog/service.go
// defaultPageSize), so this generously covers many hundreds of listings —
// far beyond any real catalog today — while still guaranteeing the loop
// below terminates even against a malformed or hostile server that keeps
// returning a non-empty next_page_token forever.
const setupBasePluginMaxPages = 25

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

// installBasePluginsForSetup is POST /api/setup's hook (ut-docs#591): looks
// up the confirmed country's free base plugins, persists them as pending
// BEFORE any network attempt (so the list survives even if the process dies
// mid-request), then makes one best-effort, time-boxed synchronous attempt
// to resolve+install right here. Mirrors this same handler's other
// best-effort steps (restore-choice, demo-data seed) — a failure here must
// never delay or fail the wizard's own response, so every error is logged
// and swallowed.
func installBasePluginsForSetup(ctx context.Context, d *common.Deps, country string) {
	specs := basePluginsForCountry(ctx, d, country)
	if len(specs) == 0 {
		return
	}
	pending := append([]basePluginSpec(nil), specs...)
	// Merge (append-if-absent), never wholesale-replace (ut-docs#1110): the
	// wizard's language step (setup_language_catalog.go) can already have
	// queued an unrelated spec — e.g. a catalog-only language picked at step
	// 1, still offline — before this country step ever runs. A plain
	// savePendingBasePlugins here silently dropped that spec.
	if err := addPendingBasePlugins(ctx, d, pending); err != nil {
		logging.L().Errorf("setup wizard: persist pending base plugins: %v", err)
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
		// active): drop just THIS spec from whatever else is pending — same
		// reasoning as above, a wholesale replace here would erase an
		// unrelated spec another step queued.
		if err := dismissPendingBasePlugin(ctx, d, spec.CanonicalType, spec.Locale); err != nil {
			logging.L().Errorf("setup wizard: clear installed pending base plugin %s/%s: %v",
				spec.CanonicalType, spec.Locale, err)
		}
	}
}

// addPendingBasePlugins merges specs into the persisted pending list
// (append-if-absent), unlike savePendingBasePlugins' wholesale replace — see
// installBasePluginsForSetup's own doc comment for why this matters. An
// unreadable existing list (corrupt JSON) is replaced rather than kept:
// persisting these specs matters more than preserving bytes no reader can
// parse anyway.
func addPendingBasePlugins(ctx context.Context, d *common.Deps, specs []basePluginSpec) error {
	pending, err := loadPendingBasePlugins(ctx, d)
	if err != nil {
		logging.L().Warnf("setup wizard: pending base plugins unreadable, resetting list: %v", err)
		pending = nil
	}
	seen := make(map[basePluginSpec]bool, len(pending))
	for _, s := range pending {
		seen[s] = true
	}
	changed := false
	for _, s := range specs {
		if seen[s] {
			continue
		}
		seen[s] = true
		pending = append(pending, s)
		changed = true
	}
	if !changed {
		return nil
	}
	return savePendingBasePlugins(ctx, d, pending)
}

// errBasePluginNotPublished is resolveAndInstallBasePlugin's "the catalog
// answered but has no listing for this spec" for a tax spec, which stays
// pending (ut-docs#3210). A language spec keeps the silent nil no-op.
var errBasePluginNotPublished = errors.New("no matching listing published in the catalog")

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
// Returns nil when there's genuinely nothing to do (no language listing
// published yet, or an equivalent plugin is already active — idempotent on
// a retry or a second wizard run) or a non-nil error describing why the
// spec should stay pending for the next attempt — including
// errBasePluginNotPublished for a tax spec with no listing (ut-docs#3210).
//
// Pages through the full result under the same ctx deadline as a single
// page (ut-docs#2133): this used to read only page 1 of ListPlugins, unlike
// its two setup-wizard siblings (setupLanguageCatalogEntries,
// setupTaxCatalogEntries), which both already follow next_page_token
// (ut-docs#1108). ut-cloud's real defaultPageSize is 20 and compatibility
// filtering runs server-side before pagination, so a legitimate match can
// sort past page 1 as the catalog grows — and because "nothing published
// for this country yet" is a legitimate, silent no-op one level down, a
// single-page fetch would silently install nothing and report no error.
// Bounded by setupBasePluginMaxPages so a malformed/hostile server can't
// loop forever.
func resolveAndInstallBasePlugin(ctx context.Context, d *common.Deps, spec basePluginSpec) error {
	// enroll.Effective, NOT EnsureRegistered (ut-docs#2964 review): looking
	// the pack up is a browse, and ADR-0015 lets only a real download/install
	// mint the shop's cloud store identity. Since basePluginsForCountry
	// queues a spec for every country whose language core doesn't bundle,
	// registering here enrolled FR/IT/NL/PK tills at setup with no pack to
	// install. cloudInstallPluginVersion below still calls EnsureRegistered
	// itself, so the ADR's trigger 1 is unchanged.
	effCfg := enroll.Effective(d.Cfg)
	client := marketplace.NewClient(&effCfg.Marketplace, oauth.NewTokenClient(&effCfg.Marketplace))

	var all []marketplace.PluginSummary
	pageToken := ""
	for page := 0; page < setupBasePluginMaxPages; page++ {
		resp, err := client.ListPlugins(ctx, &marketplace.ListPluginsRequest{Locale: spec.Locale, PageToken: pageToken})
		if err != nil {
			return fmt.Errorf("catalog unreachable: %w", err)
		}
		all = append(all, resp.Plugins...)
		if resp.NextPageToken == "" {
			break
		}
		pageToken = resp.NextPageToken
	}

	var best *marketplace.PluginSummary
	for i := range all {
		p := &all[i]
		if p.CanonicalType != spec.CanonicalType || !localeInList(p.AvailableLocales, spec.Locale) {
			continue
		}
		if best == nil || updates.Newer(p.Version, best.Version) {
			best = p
		}
	}
	if best == nil {
		if spec.CanonicalType == "tax" {
			// ut-docs#3210: a tax spec is only ever queued with the
			// operator's consent to the fiscal plugin (#1506, #1512), so
			// "nothing published" (a staging catalog, an unpublished
			// listing) must keep it pending — dropping it would leave the
			// till with no fiscal plugin and no Settings chip.
			return errBasePluginNotPublished
		}
		// Nothing published for this country/locale yet — not an error, just
		// nothing to install (mirrors the "country with nothing mapped"
		// no-op, one level down).
		return nil
	}

	listingID := best.ListingID
	if listingID == "" {
		listingID = best.ID
	}
	// Idempotency check, keyed by LISTING id, not manifest plugin id
	// (ut-docs#1063). PluginRepo.PluginActive looks up the `plugins` table's
	// manifest-plugin-id primary key — but against the real wire, a catalog
	// listing's `id` and `listing_id` are the same listing UUID (no separate
	// manifest id field on PluginSummary; confirmed by the cross-repo
	// contract test), so a check keyed on best.ID/best.ListingID could never
	// match a `plugins.id` row and this short-circuit was always false in
	// production. plugins.InstallStatusStore is already keyed by listing id —
	// the same identity cloudInstallPluginVersion itself uses for its own
	// upgrade/idempotency bookkeeping below — so ask it directly whether this
	// exact listing is already installed and active, instead of re-deriving a
	// plugin id we don't actually have yet. PluginID != "" mirrors the same
	// two-part Active check cloudInstallPluginVersion's own priorInstalled
	// and sync_admin.go's convergePluginSet both apply to this store's
	// records (review note, ut-docs#1063) — an Active record with a blank
	// PluginID isn't reachable today (every writer of Active sets it), but
	// matching the sibling checks costs nothing and keeps the three from
	// silently drifting apart.
	if status, hadStatus, statusErr := plugins.NewInstallStatusStore(d.Db).Get(ctx, listingID); statusErr == nil && hadStatus && status.State == plugins.InstallStateActive && status.PluginID != "" {
		// idempotent: this listing is already installed and active. Still
		// run the ut-docs#1074 locale catch-up below rather than returning
		// early — a prior call may have installed the pack without the
		// locale ever having been applied (e.g. it became "safe to preset"
		// only after this listing was already active from an earlier run).
		if spec.CanonicalType == "language" {
			applyDerivedLocaleIfLanguagePackNowAvailable(ctx, d, spec.Locale)
		}
		return nil
	}

	if _, err := cloudInstallPluginVersion(ctx, d, listingID, best.Version); err != nil {
		return fmt.Errorf("install %s@%s: %w", listingID, best.Version, err)
	}
	if spec.CanonicalType == "language" {
		applyDerivedLocaleIfLanguagePackNowAvailable(ctx, d, spec.Locale)
	}
	return nil
}

// applyDerivedLocaleIfLanguagePackNowAvailable is ut-docs#1074's catch-up
// half of ut-docs#1027: at country-selection time, localeSafeToPreset
// (country_settings_page.go) deliberately leaves an RTL locale
// (country_settings.DefaultLocale) unapplied when its base language pack
// isn't installed yet — see that function's own doc comment. Nothing
// re-checked that once the pack actually finished installing, until now.
// Called from resolveAndInstallBasePlugin right after a "language"-type
// spec is confirmed installed-and-active (fresh install or the idempotent
// already-active branch), so it covers all three paths that reach that
// function: the setup wizard's synchronous attempt
// (installBasePluginsForSetup), the background retry (basePluginRetryTick),
// and an operator manually installing a language pack (the wizard's own
// step-1 catalog tile, setup_language_catalog.go's
// setupLanguageInstallHandler).
//
// Never overrides an operator's own explicit choice
// (common.KeyLocaleConfirmed, set only by Settings' Language card), and
// only applies when the just-installed locale is actually the shop's
// current country's own configured default — installing an unrelated
// second language (browsing, a multi-lingual staff member) must never
// silently switch the shop's default away from what the operator's country
// choice already implies.
func applyDerivedLocaleIfLanguagePackNowAvailable(ctx context.Context, d *common.Deps, installedLocale string) {
	st := d.CurrentState()
	if st.Country == "" {
		return
	}
	if tillFollowsMain(ctx, d) {
		// ut-docs#2998: store.locale is shop-wide. On an additional till the
		// main till's value arrives with the next pull; deriving it here
		// would be reverted by that pull.
		return
	}
	confirmed, _, err := d.Settings.Get(ctx, common.KeyLocaleConfirmed)
	if err != nil {
		logging.L().Warnf("base plugin install: read %s: %v", common.KeyLocaleConfirmed, err)
		return
	}
	if confirmed == "true" {
		return
	}
	cs, ok, err := data.NewCountrySettingsRepo(d.Db).Get(ctx, st.Country)
	if err != nil || !ok || cs.DefaultLocale == "" {
		return
	}
	if baseLang(strings.TrimSpace(cs.DefaultLocale)) != baseLang(strings.TrimSpace(installedLocale)) {
		return // an unrelated language pack — never clobber the derived default
	}
	if st.Locale == cs.DefaultLocale {
		return // already applied
	}
	if !localeSafeToPreset(cs.DefaultLocale) {
		// Can genuinely happen: localeInList (this file) matches a listing
		// by base language, so a pack that ships e.g. locales/fa-IR.json
		// rather than fa.json satisfies the resolve step above but not
		// localeSafeToPreset's exact-code check against
		// httpx.AvailableLocales() — silently staying pending forever with
		// no signal otherwise.
		logging.L().Warnf("base plugin install: %s installed but %s still not safe to preset (available: %v)",
			installedLocale, cs.DefaultLocale, httpx.AvailableLocales())
		return
	}
	// Build the candidate on the CURRENT state and persist-then-commit
	// (common.SaveState, then d.SetState) — never d.UpdateState first: that
	// would commit the new locale into shared memory before the DB write
	// even succeeds, so a failed SaveState (disk full, SQLITE_BUSY) would
	// leave d.State changed but the DB unchanged, silently riding along on
	// the next unrelated successful save. Same failure Deps.SetState's own
	// doc comment warns about, and the exact pattern every sibling handler
	// in this package already follows (see settings_page.go's country/
	// currency/locale handlers).
	cand := st
	cand.Locale = cs.DefaultLocale
	// settings-write:allow derived store.locale after a language-pack install; main till only (an additional till returns above, ut-docs#2998)
	if err := common.SaveState(ctx, d.Settings, cand); err != nil {
		logging.L().Errorf("base plugin install: persist derived locale: %v", err)
		return
	}
	d.SetState(cand)
	httpx.SetDefaultLocale(cand.Locale)
}

// backfillLocaleConfirmedForDivergedPendingTills is ut-docs#1892's boot-time
// backfill for a gap ut-docs#1074 itself deferred (that review's finding
// F4): common.KeyLocaleConfirmed is write-forward-only, with no backfill for
// a shop that manually diverged store.locale away from its country's
// default BEFORE the flag existed. Concretely: a shop set up offline still
// has a "language" spec sitting in KeyPendingBasePlugins; the operator
// meanwhile changed store.locale by hand, pre-#1074, so KeyLocaleConfirmed
// was never set (it didn't exist yet). Once the pending pack finally
// installs, applyDerivedLocaleIfLanguagePackNowAvailable would silently flip
// the locale back to a country default — an operator choice overridden with
// no action and no audit trail. Runs on every boot, not literally once —
// see the doc comment on its own early-return above for why "once" already
// means "until it actually needs to fire": it's a no-op the instant
// KeyLocaleConfirmed is set, by this function or by a genuine choice.
//
// The heuristic #1074's own review explored and rejected was "store.locale
// diverges from the CURRENT country's default" alone — that also matches a
// perfectly ordinary, never-touched shop still waiting on an RTL pack
// (localeSafeToPreset leaves an RTL locale unapplied until its pack is
// available, so store.locale sits at the compiled bundled default,
// necessarily different from the country's own RTL default): marking THAT
// shop confirmed would defeat ut-docs#1074's entire purpose for it.
//
// This version closes that gap with two checks, not one — a locale is
// treated as "automatic" (never a signal of a manual choice) when it
// matches EITHER:
//  1. The COMPILED default, d.Cfg.CompiledDefaultLocale — deliberately NOT
//     d.Cfg.Locales.Locale, which internal/settings.Store.LoadRuntimeConfig
//     overwrites with the shop's own persisted store.locale on every boot
//     after the first (app.go calls it before pages.Init ever runs). Using
//     Locales.Locale here would make this comparison trivially true on
//     every real till — st.Locale IS Locales.Locale by the time Init sees
//     it — so the backfill would never fire outside a test fixture that
//     happens to skip that mutation. Caught in review (ut-docs#1892 review,
//     finding F1) via a probe that replicated production boot ordering;
//     TestBackfillLocaleConfirmed_SurvivesProductionBootOrdering below locks
//     it in.
//  2. ANY known country's own DefaultLocale, not just the shop's CURRENT
//     country's — a merchant who changes country while an RTL pack is still
//     pending leaves store.locale at the PREVIOUS country's default
//     (derivation to the new country's default is skipped exactly the same
//     way the never-touched case is, so nothing rewrites the stale value).
//     That leftover is still automatic, not a manual choice, and checking
//     only the current country's default would misclassify it and
//     permanently disable the catch-up for the shop this card exists to
//     protect (ut-docs#1892 review, finding F2c).
//     TestBackfillLocaleConfirmed_LeavesLeftoverPreviousCountryDefaultUnconfirmed
//     below locks this in.
//
// A locale matching neither is the only remaining explanation left standing
// (see common.KeyLocaleConfirmed's own doc comment): a manual write via
// Settings' Language card, POST /api/settings/upsert, or a cloud
// set_setting directive (ut-docs#1892 review, finding F2ab) — every one of
// those IS a genuine operator/remote choice, so correctly backfilling it is
// the intended behaviour, not a gap.
//
// Known accepted residual gap (ut-docs#1892 review, finding F3, same
// "bounded, not urgent" framing the original card used): an operator who
// deliberately chose exactly the compiled default locale (e.g. an
// English-speaking owner of a non-English-default shop picking en-US on
// purpose) is indistinguishable from a shop that never touched the setting
// at all — there is no third signal in this schema to tell them apart. That
// shop stays exposed to exactly the override #1074 describes. Left
// unresolved deliberately rather than invasively, per the same reasoning
// #1892's own card body already used to accept the original gap.
//
// Runs synchronously, before StartBasePluginRetry's background loop can
// ever reach the code path this protects against — ordering matters here,
// not just eventual consistency.
func backfillLocaleConfirmedForDivergedPendingTills(ctx context.Context, d *common.Deps) {
	if tillFollowsMain(ctx, d) {
		return // ut-docs#2998: shop-wide flag; the main till's value wins at the next pull
	}
	confirmed, _, err := d.Settings.Get(ctx, common.KeyLocaleConfirmed)
	if err != nil {
		logging.L().Warnf("locale-confirmed backfill: read %s: %v", common.KeyLocaleConfirmed, err)
		return
	}
	if confirmed == "true" {
		return // already set — a genuine prior choice, or an earlier boot
		// already ran this backfill. Idempotent either way.
	}
	pending, err := loadPendingBasePlugins(ctx, d)
	if err != nil {
		logging.L().Warnf("locale-confirmed backfill: load pending base plugins: %v", err)
		return
	}
	hasPendingLanguage := false
	for _, s := range pending {
		if s.CanonicalType == "language" {
			hasPendingLanguage = true
			break
		}
	}
	if !hasPendingLanguage {
		return // nothing pending that could ever trigger the derive-on-install
		// path this backfill exists to protect against.
	}
	st := d.CurrentState()
	if st.Country == "" || st.Locale == "" {
		return
	}
	if st.Locale == strings.TrimSpace(d.Cfg.CompiledDefaultLocale) {
		return // never touched — see check 1 in the doc comment above.
	}
	countries, err := data.NewCountrySettingsRepo(d.Db).List(ctx)
	if err != nil {
		logging.L().Warnf("locale-confirmed backfill: list country settings: %v", err)
		return
	}
	for _, c := range countries {
		if c.DefaultLocale != "" && st.Locale == c.DefaultLocale {
			return // matches some country's own default — current or a
			// leftover from a previous one; see check 2 in the doc
			// comment above.
		}
	}
	// settings-write:allow one-time boot backfill of a flag derived from this till's own locale; main till only (an additional till returns at the top, ut-docs#2998)
	if err := d.Settings.Set(ctx, common.KeyLocaleConfirmed, "true"); err != nil {
		logging.L().Errorf("locale-confirmed backfill: persist %s: %v", common.KeyLocaleConfirmed, err)
		return
	}
	logging.L().Infof("locale-confirmed backfill: marked %s for country=%s locale=%s (matches neither the compiled default nor any known country's default) — a pending language-pack install would otherwise have silently overridden it",
		common.KeyLocaleConfirmed, st.Country, st.Locale)
}

// languagePackLocalesForListing looks up the marketplace catalog for the
// AvailableLocales of a canonical_type "language" listing (ut-docs#1893).
// resolveAndInstallBasePlugin already knows a listing's locale because it
// starts from a basePluginSpec carrying one; a marketplace-store install
// (plugin_api.go's handleInstallFromMarketplace) starts from nothing but a
// listing id, so this re-derives it the same way resolveAndInstallBasePlugin
// resolves listingID: matched against either the summary's ID or its
// ListingID field, since the two already converge to the same value
// (PluginSummary.UnmarshalJSON's firstNonEmptyStr fallback either way).
// Returns ok=false — never an error — for "not a language pack", "listing
// not found", or a catalog call failure: this is a best-effort lookup for a
// plugin that's already finished installing, so a marketplace hiccup here
// must never be surfaced as an install failure.
//
// Follows NextPageToken the same way setup_language_catalog.go's own
// catalog fetch does (ut-docs#1108) — the real ut-cloud catalog paginates
// at ~20 listings/page, and a single-page fetch here would silently miss
// this locale catch-up for any "language" listing beyond page 1. Bounded by
// the same setupLanguageCatalogMaxPages cap for the same reason: far beyond
// any real catalog today, while still guaranteeing termination against a
// malformed/hostile server that keeps returning a non-empty token forever.
func languagePackLocalesForListing(ctx context.Context, client *marketplace.Client, listingID string) ([]string, bool) {
	pageToken := ""
	for page := 0; page < setupLanguageCatalogMaxPages; page++ {
		resp, err := client.ListPlugins(ctx, &marketplace.ListPluginsRequest{Capability: []string{"language"}, PageToken: pageToken})
		if err != nil {
			logging.L().Warnf("base plugin install: locale catch-up: list catalog for %s: %v", listingID, err)
			return nil, false
		}
		for i := range resp.Plugins {
			p := &resp.Plugins[i]
			if p.CanonicalType != "language" {
				continue
			}
			if p.ID == listingID || p.ListingID == listingID {
				return p.AvailableLocales, true
			}
		}
		if resp.NextPageToken == "" {
			return nil, false
		}
		pageToken = resp.NextPageToken
	}
	return nil, false
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
// still pending, attempt each once more, and remove exactly what installed.
// Silent-and-retry on failure, same posture as internal/updates' checkOnce —
// this never surfaces an error to a caller, only to the log and the
// Settings chip (via the persisted pending list itself).
//
// Removes via removePendingBasePlugins rather than a wholesale
// savePendingBasePlugins(remaining) (ut-docs#1117, the same clobber pattern
// ut-docs#1110 already fixed in installBasePluginsForSetup — just a wider
// window: a full catalog fetch + install per spec sits between this tick's
// own read and write, versus a single in-process call). A spec another
// writer queues while this tick is mid-flight (e.g. POST /api/setup racing
// the 5-minute tick) must survive the tick's write, not get silently wiped
// by a save of this tick's now-stale snapshot.
func basePluginRetryTick(ctx context.Context, d *common.Deps) {
	pending, err := loadPendingBasePlugins(ctx, d)
	if err != nil {
		logging.L().Warnf("base plugin retry: load pending list: %v", err)
		return
	}
	if len(pending) == 0 {
		return
	}
	var installed []basePluginSpec
	for _, spec := range pending {
		if err := resolveAndInstallBasePlugin(ctx, d, spec); err != nil {
			logging.L().Infof("base plugin retry: %s/%s still pending: %v", spec.CanonicalType, spec.Locale, err)
			continue
		}
		installed = append(installed, spec)
	}
	if len(installed) > 0 {
		if err := removePendingBasePlugins(ctx, d, installed); err != nil {
			logging.L().Errorf("base plugin retry: persist pending list: %v", err)
		}
	}
}

// removePendingBasePlugins drops exactly the given specs from the persisted
// pending list — re-reading it fresh first so a spec another writer queued
// between the caller's own read and this write (see basePluginRetryTick)
// is preserved rather than clobbered, the same merge-safe-write reasoning
// addPendingBasePlugins already applies on the add side. Symmetric with
// dismissPendingBasePlugin, just batched: dismiss removes one spec a
// merchant chose from Settings; this removes every spec a retry pass
// actually installed in one call.
func removePendingBasePlugins(ctx context.Context, d *common.Deps, installed []basePluginSpec) error {
	if len(installed) == 0 {
		return nil
	}
	pending, err := loadPendingBasePlugins(ctx, d)
	if err != nil {
		return err
	}
	drop := make(map[basePluginSpec]bool, len(installed))
	for _, s := range installed {
		drop[s] = true
	}
	remaining := make([]basePluginSpec, 0, len(pending))
	changed := false
	for _, s := range pending {
		if drop[s] {
			changed = true
			continue
		}
		remaining = append(remaining, s)
	}
	if !changed {
		return nil
	}
	return savePendingBasePlugins(ctx, d, remaining)
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
		defer logging.RecoverAndLog("pages.basePluginRetry")
		defer wg.Done()
		select {
		case <-time.After(basePluginRetryInitialDelay):
		case <-basePluginRetryNudge:
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
			case <-basePluginRetryNudge:
				basePluginRetryTick(ctx, d)
			case <-ctx.Done():
				return
			}
		}
	}()
}

// queueBasePluginsForCountryChange is the post-setup counterpart of
// installBasePluginsForSetup (ut-docs#1068): every writer of store.country
// after the wizard (POST /api/settings/save, POST /api/settings/upsert and
// the cloud SetSetting directive) calls it once the new country is
// persisted, and only on a real change — so a pack the merchant dismissed
// is not re-queued by re-saving the same country. It only queues (merge-
// safe, like the wizard) and nudges the background retry: the caller never
// waits on the network, and an offline till keeps the spec pending exactly
// as the wizard's offline path does. A failure is logged, never returned —
// the country change itself has already succeeded.
func queueBasePluginsForCountryChange(ctx context.Context, d *common.Deps, country string) {
	specs := basePluginsForCountry(ctx, d, country)
	if len(specs) == 0 {
		return
	}
	if err := addPendingBasePlugins(ctx, d, append([]basePluginSpec(nil), specs...)); err != nil {
		logging.L().Errorf("country change: queue base plugins for %s: %v", country, err)
		return
	}
	nudgeBasePluginRetry()
}

// basePluginRetryNudge wakes StartBasePluginRetry's loop for an immediate
// pass (ut-docs#1068) instead of waiting out its initial delay or the
// 5-minute interval. Package-level because a till process runs exactly one
// retry loop; buffered 1 so any number of nudges before the loop runs
// collapse into one pass.
var basePluginRetryNudge = make(chan struct{}, 1)

// nudgeBasePluginRetry never blocks: a pass already pending covers it.
func nudgeBasePluginRetry() {
	select {
	case basePluginRetryNudge <- struct{}{}:
	default:
	}
}
