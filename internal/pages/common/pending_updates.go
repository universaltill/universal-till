package common

import (
	"context"

	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
)

// RefreshPendingUpdates recomputes and republishes the status-bar chip's
// pending-update count immediately after a plugin lifecycle change
// (ut-docs#2787). Before this, the chip only got a fresh number from
// StartPluginUpdateScheduler's own tick (30s after boot, then every 15
// minutes — internal/pages/plugin_update_scheduler.go), so a manual
// install/update/uninstall/rollback/import, a cloud-directed install/
// uninstall (cloudsync_wire.go) or a store install could leave the chip
// stale for up to that long. ReloadPlugins calls this on every lifecycle
// change, so the chip reflects reality right away instead.
//
// Counts exactly like pluginUpdateCheckTick's steady state, WITHOUT
// applying anything — this never installs, it only reads the local catalog
// cache and reports. Nil-safe / no-op when the deps aren't fully wired
// (CatalogRepo or Db nil), matching pluginUpdateCheckTick's own guard.
func (d *Deps) RefreshPendingUpdates(ctx context.Context) {
	log := logging.L()
	// Same recover() shape as pluginUpdateCheckTick: this runs inline from
	// ReloadPlugins, which fires from HTTP handlers and the sync-pull
	// goroutine alike, so a panic here must never take down the till
	// mid-sale — log it and leave the previously published status in place.
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("[RefreshPendingUpdates] recovered from panic: %v", r)
		}
	}()
	if d.CatalogRepo == nil || d.Db == nil {
		return
	}

	mainTillURL := d.SyncPrimaryURL(ctx)
	isReplica := mainTillURL != ""

	var defaultLocale string
	if d.Cfg != nil {
		defaultLocale = d.Cfg.DefaultLocale
	}
	locale, deviceArch := marketplace.TillCatalogKey(defaultLocale)
	checker := plugins.NewUpdateChecker(d.Db, d.CatalogRepo, locale, deviceArch)
	found, err := checker.CheckForUpdates(ctx)
	if err != nil {
		log.Warnf("[RefreshPendingUpdates] check failed: %v", err)
		return
	}

	pending := 0
	languagePending := false
	for _, u := range found {
		isLanguage := u.CanonicalType == plugins.CanonicalTypeLanguage
		if isReplica || !isLanguage {
			pending++
			if isLanguage {
				languagePending = true
			}
			continue
		}
		// On a main/standalone till, a language-pack update is exactly what
		// the scheduler auto-applies itself (pluginUpdateCheckTick) — this
		// is a read-only refresh, so it doesn't apply anything here. If the
		// scheduler's own next tick fails to apply it, that tick will count
		// it again; under-counting briefly is the right direction to be
		// wrong in: a chip that disappears a little early is a far smaller
		// sin on a till than one that won't go away.
	}
	plugins.PublishPendingUpdates(plugins.PendingUpdateStatus{
		Count:           pending,
		LanguagePending: languagePending,
		MainTillURL:     mainTillURL,
	})
}
