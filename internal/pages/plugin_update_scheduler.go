package pages

import (
	"context"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
)

const (
	// pluginUpdateCanonicalTypeLanguage is the one canonical type (per
	// ADR-0002's plugin taxonomy) this scheduler auto-applies without asking. A
	// language pack is content, not code, and a stale one silently degrades
	// the product for exactly the non-English merchants we are trying to
	// win (ut-docs#1953) — every other type only ever surfaces as a pending
	// count; the merchant still applies those manually from /plugins.
	pluginUpdateCanonicalTypeLanguage = "language"
)

// pluginUpdateCheckInitialDelay/pluginUpdateCheckInterval are vars, not
// consts, so a test can shrink both to observe StartPluginUpdateScheduler's
// actual timing (ut-docs#2299) — the production values (30s, 15m) are
// unchanged. Restore both (e.g. via t.Cleanup) after overriding.
var (
	pluginUpdateCheckInitialDelay = 30 * time.Second
	pluginUpdateCheckInterval     = 15 * time.Minute
)

// pluginApplyUpdateFn is applyPluginUpdate, indirected through a var so a
// test can fake the install outcome (success/failure) without a real
// signed marketplace artifact — same seam shape as update_api.go's
// autoUpdateApply. Restore the original value (e.g. via t.Cleanup) after
// overriding it.
var pluginApplyUpdateFn = applyPluginUpdate

// pluginUpdateTickFn is pluginUpdateCheckTick, indirected through a var so a
// test can observe WHEN StartPluginUpdateScheduler invokes it (e.g. a
// counting/signalling stub) without needing a real *common.Deps with a live
// DB and catalog repo — same seam shape as pluginApplyUpdateFn above.
// Restore the original value (e.g. via t.Cleanup) after overriding it.
var pluginUpdateTickFn = pluginUpdateCheckTick

// StartPluginUpdateScheduler runs the background installed-plugin update
// check (ut-docs#1953). Before this, plugins.UpdateChecker.CheckForUpdates
// had exactly one caller — the manual "Check for updates" button on
// /plugins — so a merchant who never opened that page never learned an
// update existed, and a language pack shipped weeks ago never reached
// their till. Shape mirrors StartBasePluginRetry exactly: a goroutine, a
// short initial delay, then a ticker, silent-and-retry, wg.Done() on
// ctx.Done(). Offline-first: CheckForUpdates reads the LOCAL catalog cache
// only — internal/server's own BackgroundJobs already refreshes that cache
// on its own schedule — so a tick never makes a network call, never blocks
// checkout, and any failure here is logged, never surfaced to the merchant
// mid-sale.
func StartPluginUpdateScheduler(ctx context.Context, d *common.Deps, wg *sync.WaitGroup) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		select {
		case <-time.After(pluginUpdateCheckInitialDelay):
		case <-ctx.Done():
			return
		}
		pluginUpdateTickFn(ctx, d)
		t := time.NewTicker(pluginUpdateCheckInterval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				pluginUpdateTickFn(ctx, d)
			case <-ctx.Done():
				return
			}
		}
	}()
}

// pluginUpdateCheckTick runs one scheduler pass: auto-applies every
// language-pack update found via the shared applyPluginUpdate path (same
// one the manual Update button uses), and leaves everything else — plus any
// language-pack update that failed to apply — as a pending count for the
// merchant to review manually from /plugins. Keeping the auto-apply set to
// "content, not code" is a deliberate, narrow product decision (ut-docs#1953),
// not an oversight: no other plugin type ever auto-applies without a human
// having a say.
func pluginUpdateCheckTick(ctx context.Context, d *common.Deps) {
	log := logging.L()
	// A recover() at the top keeps the offline-first "a background check
	// never disturbs the sale" promise honest, for the same reason
	// syncPullPlugins has one: this tick drives the very same
	// install-and-reload path, and an unrecovered panic in a goroutine
	// takes down the WHOLE till process — mid-sale, on a merchant's
	// counter — not just this loop. Log it and let the next tick retry
	// (ut-docs#1953 review).
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("[PluginUpdateScheduler] recovered from panic (will retry next tick): %v", r)
		}
	}()
	if d.CatalogRepo == nil {
		return
	}

	// A replica till never applies a plugin update locally (ut-docs#460) —
	// applyPluginUpdate would just fail the replica guard for every one of
	// them. Still worth counting what's pending so a status chip can point
	// the operator at the primary, so only the auto-apply step below is
	// skipped for a replica, not the whole tick.
	isReplica := d.SyncPrimaryURL(ctx) != ""

	checker := plugins.NewUpdateChecker(d.Db, d.CatalogRepo)
	found, err := checker.CheckForUpdates(ctx)
	if err != nil {
		log.Warnf("[PluginUpdateScheduler] check failed: %v", err)
		return
	}

	pending := 0
	languagePending := false
	for _, u := range found {
		isLanguage := u.CanonicalType == pluginUpdateCanonicalTypeLanguage
		if isReplica || !isLanguage {
			pending++
			if isLanguage {
				// A joined till never applies a language-pack update itself
				// (ut-docs#460) — it waits for the main till, so this is
				// pending indefinitely rather than for one tick, and the
				// merchant deserves to know it's specifically the language
				// pack that's behind (ut-docs#2299), not just "N updates".
				languagePending = true
			}
			continue
		}
		if _, _, err := pluginApplyUpdateFn(ctx, d, u.PluginID); err != nil {
			log.Warnf("[PluginUpdateScheduler] auto-update of %s failed: %v", u.PluginID, err)
			pending++ // surface it rather than silently dropping a failed language-pack update
			languagePending = true
			continue
		}
		log.Infof("[PluginUpdateScheduler] auto-applied %s %s -> %s", u.PluginID, u.InstalledVersion, u.AvailableVersion)
	}
	plugins.SetPendingUpdates(pending, languagePending)
}
