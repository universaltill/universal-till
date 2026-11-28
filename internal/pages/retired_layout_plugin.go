package pages

import (
	"context"
	"database/sql"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/settings"
	"github.com/universaltill/universal-till/internal/shoptemplate"
)

// retiredLayoutPluginIDs are the built-in layout plugins that shop-type
// templates replaced (ADR-0190, ut-docs#6332): the salon layout is now the
// service template's legacy_layout / classes, and the plugin is no longer
// shipped. Earlier builds installed it for every service shop.
var retiredLayoutPluginIDs = []string{"com.universaltill.layout-salon"} // core-neutral:allow retired plugin id: the migration must name what it removes, and removes it only from tills that still carry it

// removeRetiredLayoutPlugins is the one-time migration that uninstalls a
// retired built-in layout plugin left on a till by an earlier build (DB rows,
// enabled state, files), exactly as a manual uninstall would. Idempotent: a
// till without it does nothing. It only removes a plugin the till installed
// itself (trust level "system"); a same-id plugin from anywhere else is left
// alone. Reports whether anything was removed so the caller reloads plugins.
// Best-effort: a failure is logged and retried on the next boot, never fatal.
//
// A plugin the manager had switched off (installed, inactive) is an opt-out
// from that layout: it is recorded as shoptemplate.LegacyLayoutOffKey before
// the removal so the template's legacy_layout does not bring the layout back
// (ADR-0190 Q1). If the marker cannot be written the plugin is kept for the
// next boot rather than lose the opt-out.
func removeRetiredLayoutPlugins(ctx context.Context, db *sql.DB, st *settings.Store) (removed bool) {
	repo := data.NewPluginRepo(db)
	for _, id := range retiredLayoutPluginIDs {
		trust, found, err := repo.PluginTrustLevel(ctx, id)
		if err != nil {
			logging.L().Warnf("boot: could not look up retired layout plugin %s: %v", id, err)
			continue
		}
		if !found || trust != "system" {
			continue
		}
		if active, err := repo.PluginActive(ctx, id); err != nil {
			logging.L().Warnf("boot: could not read the state of retired layout plugin %s: %v", id, err)
			continue
		} else if !active && st != nil {
			// settings-write:allow boot-time migration marker; an additional till's copy is replaced by the main till's on the next pull
			if err := st.Set(ctx, shoptemplate.LegacyLayoutOffKey, "1"); err != nil {
				logging.L().Warnf("boot: could not record the layout opt-out for %s: %v", id, err)
				continue
			}
		}
		if err := plugins.UninstallPluginTree(ctx, db, paths.Plugins(), id); err != nil {
			logging.L().Warnf("boot: could not remove retired layout plugin %s: %v", id, err)
			continue
		}
		logging.L().Infof("boot: removed retired built-in layout plugin %s (replaced by shop-type templates)", id)
		removed = true
	}
	return removed
}
