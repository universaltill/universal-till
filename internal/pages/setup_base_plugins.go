package pages

import (
	"context"
	"encoding/json"
	"fmt"
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
	if err := savePendingBasePlugins(ctx, d, pending); err != nil {
		logging.L().Errorf("setup wizard: persist pending base plugins: %v", err)
	}

	attemptCtx, cancel := context.WithTimeout(ctx, setupBasePluginAttemptTimeout)
	defer cancel()
	remaining := make([]basePluginSpec, 0, len(pending))
	for _, spec := range pending {
		if err := resolveAndInstallBasePlugin(attemptCtx, d, spec); err != nil {
			logging.L().Warnf("setup wizard: base plugin %s/%s not installed yet, will retry in background: %v",
				spec.CanonicalType, spec.Locale, err)
			remaining = append(remaining, spec)
		}
	}
	if len(remaining) != len(pending) {
		if err := savePendingBasePlugins(ctx, d, remaining); err != nil {
			logging.L().Errorf("setup wizard: persist pending base plugins after attempt: %v", err)
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

// localeInList reports whether want is present in locales, compared
// case-insensitively — locale tags are case-insensitive ("de" == "DE"), and
// the catalog's casing is the server's choice, not a contract.
func localeInList(locales []string, want string) bool {
	for _, l := range locales {
		if strings.EqualFold(l, want) {
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
