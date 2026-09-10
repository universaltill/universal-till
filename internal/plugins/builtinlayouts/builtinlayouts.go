// Package builtinlayouts wires the ADR-0026 shop_type setting
// (internal/pages/setup_page.go's setupShopTypes, changeable later in
// Settings) to the ADR-0088 declarative UI slot registry: the till's own
// `layout` plugins that ship inside the binary rather than through the
// marketplace (ut-docs#1902).
//
// Both mechanisms already existed and were already fully built in
// isolation before this package — shop_type was captured and changeable
// but drove nothing; plugins/layout-salon was a real, working `layout`
// manifest built as ut-docs#1904's own e2e proof but never installed
// outside that e2e fixture. This package is the connection between them,
// nothing else.
package builtinlayouts

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/plugins"
	layoutsalon "github.com/universaltill/universal-till/plugins/layout-salon"
)

// SalonPluginID is plugins/layout-salon/plugin.json's declared id.
const SalonPluginID = "com.universaltill.layout-salon"

// pluginForShopType maps an ADR-0026 shop_type value to the builtin layout
// plugin id that should be active for it, or "" for none. Only "service"
// (SumUp's own "service trade" bucket — a salon/barber) has a builtin pack
// today; cafe/retail/hospitality/market_stall/other keep the byte-identical,
// everything-visible menu. A retail/hospitality pack is real, separate
// follow-up work (ut-docs#1902's own scoped-down BA/Architect passes), not
// fabricated here to fill every tile.
func pluginForShopType(shopType string) string {
	if shopType == "service" {
		return SalonPluginID
	}
	return ""
}

// Sync ensures exactly the builtin layout plugin (if any) matching shopType
// is installed at its current (embedded) version, installing, upgrading or
// removing it as needed. Idempotent: calling it again with the same
// shopType and no newer embedded version is a no-op.
//
// Keyed on whether the plugin is INSTALLED at all (GetInstalledPluginVersion),
// not on whether it's currently active: a plugin an operator manually
// disabled from Settings → Plugins must still be removed when shopType moves
// away from it, or it can silently resurrect on a later re-enable with the
// shop now on a different business type. The same existence check also
// catches an embedded manifest version bump across a till self-update —
// found-but-stale-version is treated as "not correctly installed" and
// reinstalled fresh, rather than leaving the old version's files in place
// indefinitely.
//
// Sync only persists DB rows and on-disk plugin files — it never reloads the
// plugin manager's in-memory state itself, matching plugins.UninstallPlugin's
// own caller-reloads convention. Callers must call their
// common.Deps.ReloadPlugins(ctx) afterward (the same call every other plugin
// lifecycle change already makes) or the change won't show up in the menu
// until the next reload.
//
// Sync reports via its changed return whether it actually installed, removed
// or reinstalled the plugin, so a caller can skip that reload on a genuine
// no-op (ut-docs#2006) — but changed is never the caller's signal to skip a
// reload on ERROR: on the reinstall path (a stale version replaced with the
// current one), removeSalon can succeed before installSalon then fails,
// leaving the DB saying "uninstalled" while the caller's in-memory state
// still carries the pre-existing amendments. removeSalon's own doc comment
// already states file-removal failure is deliberately swallowed specifically
// so the caller's ReloadPlugins always runs — callers must apply that same
// intent to ANY error Sync returns, not only removeSalon's, and reload
// regardless (see setup_page.go/settings_page.go's shop_type handlers for
// the pattern: reload unless err == nil && !changed).
func Sync(ctx context.Context, db *sql.DB, shopType string) (changed bool, err error) {
	want := pluginForShopType(shopType)
	repo := data.NewPluginRepo(db)

	installedVersion, found, err := repo.GetInstalledPluginVersion(ctx, SalonPluginID)
	if err != nil {
		return false, fmt.Errorf("builtinlayouts: check salon layout installed: %w", err)
	}

	if want != SalonPluginID {
		if !found {
			return false, nil // already in the shop_type's right state
		}
		if err := removeSalon(ctx, db); err != nil {
			return false, err
		}
		return true, nil
	}

	m, err := plugins.ParseManifest(bytes.NewReader(layoutsalon.ManifestJSON))
	if err != nil {
		return false, fmt.Errorf("builtinlayouts: parse embedded salon manifest: %w", err)
	}
	if found && installedVersion == m.Version {
		return false, nil // already installed at the current embedded version
	}
	if found {
		// A different version is installed (a stale copy from before a
		// till self-update bumped the embedded manifest) — remove it first
		// so installSalon always lands a clean copy, never a mix of two
		// versions' locale files under paths.Plugins().
		if err := removeSalon(ctx, db); err != nil {
			return false, err
		}
	}
	if err := installSalon(ctx, db, m); err != nil {
		return false, err
	}
	return true, nil
}

// installSalon writes plugins/layout-salon's embedded locale files to disk
// (Manager.syncLocales reads them back from paths.Plugins(), not from the
// manifest bytes — ADR-0088 Decision G) and persists the manifest through
// the SAME PersistManifest every marketplace/store install uses, so this
// content gets every install-time validation (protected-key/conflict
// checks) a third-party listing would. TrustLevel "system" marks it as
// core-shipped, never fetched from the marketplace — it skips only the
// network download + bundle-signature step, which does not apply to
// content embedded in the signed till binary itself.
func installSalon(ctx context.Context, db *sql.DB, m *plugins.Manifest) error {
	localeEntries, err := fs.ReadDir(layoutsalon.Locales, "locales")
	if err != nil {
		return fmt.Errorf("builtinlayouts: read embedded salon locales: %w", err)
	}
	destDir := paths.Plugins(m.ID, m.Version, "locales")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("builtinlayouts: create %s: %w", destDir, err)
	}
	for _, e := range localeEntries {
		if e.IsDir() {
			continue
		}
		// fs.FS paths are always slash-separated (io/fs's own contract),
		// regardless of GOOS — filepath.Join here would build a
		// backslash-joined path on Windows and fs.ReadFile would return
		// "file does not exist" against the embedded FS, silently aborting
		// every install on that platform before PersistManifest ever runs.
		// destDir below is a REAL filesystem path, so filepath.Join is the
		// correct (and only correct) choice there.
		raw, err := fs.ReadFile(layoutsalon.Locales, path.Join("locales", e.Name()))
		if err != nil {
			return fmt.Errorf("builtinlayouts: read embedded locale %s: %w", e.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(destDir, e.Name()), raw, 0o644); err != nil {
			return fmt.Errorf("builtinlayouts: write locale %s: %w", e.Name(), err)
		}
	}

	if err := plugins.PersistManifest(ctx, db, m, plugins.InstallOptions{
		TrustLevel: "system",
	}); err != nil {
		return fmt.Errorf("builtinlayouts: install salon layout: %w", err)
	}
	return nil
}

// removeSalon uninstalls the salon layout the same way a manual Settings
// uninstall would (cascading DB rows + audit event), then removes its
// on-disk files so a later switch back to shopType=service reinstalls a
// clean copy rather than finding stale locale files. File removal is
// best-effort, matching handleUninstallPlugin's own "DB is the source of
// truth" convention: a locked/read-only directory must not leave the
// already-uninstalled plugin's menu amendments stuck in memory because the
// caller's ReloadPlugins never ran.
func removeSalon(ctx context.Context, db *sql.DB) error {
	if err := plugins.UninstallPlugin(ctx, db, SalonPluginID); err != nil {
		return fmt.Errorf("builtinlayouts: uninstall salon layout: %w", err)
	}
	if err := os.RemoveAll(paths.Plugins(SalonPluginID)); err != nil {
		logging.L().Warnf("builtinlayouts: could not remove salon layout files: %v", err)
	}
	return nil
}
