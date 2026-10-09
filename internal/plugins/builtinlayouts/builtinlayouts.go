// Package builtinlayouts wires the ADR-0026 shop_type setting
// (plugins.ShopTypes, offered by the setup wizard and changeable later in
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
	"sync"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/plugins"
	layoutsalon "github.com/universaltill/universal-till/plugins/layout-salon"
)

// builtin is one layout plugin embedded in the till binary: its manifest
// bytes plus the locale files PersistManifest's caller must put on disk.
type builtin struct {
	manifestJSON []byte
	locales      fs.FS
}

// builtins is every embedded builtin layout. Core never names one by plugin
// id: the builtin for a shop type is the embedded one whose manifest
// `provides` layout.shop_type:<shopType> (ADR-0129 §2, ut-docs#3178). Only
// these embedded manifests are consulted, so a marketplace plugin declaring
// the same capability is never selected nor removed.
var builtins = []builtin{
	{manifestJSON: layoutsalon.ManifestJSON, locales: layoutsalon.Locales},
}

// parse parses the embedded manifest (cheap; embedded bytes never change).
func (b builtin) parse() (*plugins.Manifest, error) {
	m, err := plugins.ParseManifest(bytes.NewReader(b.manifestJSON))
	if err != nil {
		return nil, fmt.Errorf("builtinlayouts: parse embedded layout manifest: %w", err)
	}
	return m, nil
}

// provides reports whether m declares layout.shop_type:<shopType>.
func provides(m *plugins.Manifest, shopType string) bool {
	want := plugins.CapabilityLayoutShopTypePrefix + shopType
	for _, p := range m.Provides {
		if p == want {
			return true
		}
	}
	return false
}

// PluginIDForShopType maps an ADR-0026 shop_type value to the id of the
// embedded builtin layout plugin that should be active for it, or "" for
// none. Only "service" (SumUp's own "service trade" bucket — a salon/barber)
// has a builtin pack today; cafe/retail/hospitality/market_stall/other keep
// the byte-identical, everything-visible menu. A retail/hospitality pack is
// real, separate follow-up work (ut-docs#1902's own scoped-down BA/Architect
// passes), not fabricated here to fill every tile.
func PluginIDForShopType(shopType string) (string, error) {
	for _, b := range builtins {
		m, err := b.parse()
		if err != nil {
			return "", err
		}
		if provides(m, shopType) {
			return m.ID, nil
		}
	}
	return "", nil
}

// syncMu serializes Sync: boot, the two shop_type handlers and the
// background reconcile (pages.StartShopTypeLayoutReconcile, ut-docs#2793)
// can otherwise race a remove against an install of the same plugin.
var syncMu sync.Mutex

// Sync ensures exactly the embedded builtin layout plugin (if any) providing
// layout.shop_type:<shopType> (ADR-0129 §2, ut-docs#3178) is installed at
// its current (embedded) version, installing, upgrading or removing it as
// needed. Every other embedded builtin that is installed is removed.
// Idempotent: calling it again with the same shopType and no newer embedded
// version is a no-op.
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
// current one), removeBuiltin can succeed before installBuiltin then fails,
// leaving the DB saying "uninstalled" while the caller's in-memory state
// still carries the pre-existing amendments. removeBuiltin's own doc comment
// already states file-removal failure is deliberately swallowed specifically
// so the caller's ReloadPlugins always runs — callers must apply that same
// intent to ANY error Sync returns, not only removeBuiltin's, and reload
// regardless (see setup_page.go/settings_page.go's shop_type handlers for
// the pattern: reload unless err == nil && !changed).
func Sync(ctx context.Context, db *sql.DB, shopType string) (changed bool, err error) {
	syncMu.Lock()
	defer syncMu.Unlock()
	repo := data.NewPluginRepo(db)
	wantID, err := PluginIDForShopType(shopType)
	if err != nil {
		return false, err
	}

	for _, b := range builtins {
		m, err := b.parse()
		if err != nil {
			return false, err
		}
		c, err := syncOne(ctx, db, repo, b, m, wantID != "" && m.ID == wantID)
		if err != nil {
			return false, err
		}
		changed = changed || c
	}
	return changed, nil
}

// syncOne reconciles a single embedded builtin: install or upgrade it when
// wanted, remove it when not.
func syncOne(ctx context.Context, db *sql.DB, repo *data.PluginRepo, b builtin, m *plugins.Manifest, wanted bool) (bool, error) {
	installedVersion, found, err := repo.GetInstalledPluginVersion(ctx, m.ID)
	if err != nil {
		return false, fmt.Errorf("builtinlayouts: check builtin layout %s installed: %w", m.ID, err)
	}

	if !wanted {
		if !found {
			return false, nil // already in the shop_type's right state
		}
		if err := removeBuiltin(ctx, db, m.ID); err != nil {
			return false, err
		}
		return true, nil
	}

	if found && installedVersion == m.Version {
		return false, nil // already installed at the current embedded version
	}
	keepDisabled := false
	if found {
		// A different version is installed (a stale copy from before a
		// till self-update bumped the embedded manifest) — remove it first
		// so installBuiltin always lands a clean copy, never a mix of two
		// versions' locale files under paths.Plugins(). An operator who
		// disabled it in Settings → Plugins keeps it disabled across the
		// reinstall (ut-docs#3178 review): the self-update is not theirs.
		active, err := repo.PluginActive(ctx, m.ID)
		if err != nil {
			return false, fmt.Errorf("builtinlayouts: check builtin layout %s active: %w", m.ID, err)
		}
		keepDisabled = !active
		if err := removeBuiltin(ctx, db, m.ID); err != nil {
			return false, err
		}
	}
	if err := installBuiltin(ctx, db, b, m); err != nil {
		return false, err
	}
	if keepDisabled {
		if err := repo.SetPluginActive(ctx, nil, m.ID, false); err != nil {
			return true, fmt.Errorf("builtinlayouts: keep builtin layout %s disabled: %w", m.ID, err)
		}
	}
	return true, nil
}

// installBuiltin writes an embedded builtin layout's locale files to disk
// (Manager.syncLocales reads them back from paths.Plugins(), not from the
// manifest bytes — ADR-0088 Decision G) and persists the manifest through
// the SAME PersistManifest every marketplace/store install uses, so this
// content gets every install-time validation (protected-key/conflict
// checks) a third-party listing would. TrustLevel "system" marks it as
// core-shipped, never fetched from the marketplace — it skips only the
// network download + bundle-signature step, which does not apply to
// content embedded in the signed till binary itself.
func installBuiltin(ctx context.Context, db *sql.DB, b builtin, m *plugins.Manifest) error {
	localeEntries, err := fs.ReadDir(b.locales, "locales")
	if err != nil {
		return fmt.Errorf("builtinlayouts: read embedded layout locales: %w", err)
	}
	// Hold the per-plugin tree lock from the live-dir write through
	// PersistManifest, like every other install path (ut-docs#3278): a
	// Rollback of the builtin racing this shop-type reconcile could
	// otherwise commit over the fresh install. Lock order is Sync's syncMu
	// then this lock; removeBuiltin takes and releases it (via
	// UninstallPluginTree) before installBuiltin runs, so they never nest —
	// the mutex is not reentrant. PersistManifest does not lock.
	defer plugins.LockPluginTree(paths.Plugins(), m.ID)()

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
		raw, err := fs.ReadFile(b.locales, path.Join("locales", e.Name()))
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
		return fmt.Errorf("builtinlayouts: install builtin layout %s: %w", m.ID, err)
	}
	return nil
}

// removeBuiltin uninstalls a builtin layout the same way a manual Settings
// uninstall would (cascading DB rows + audit event), then removes its
// on-disk files so a later switch back to its shop type reinstalls a
// clean copy rather than finding stale locale files. File removal is
// best-effort, matching handleUninstallPlugin's own "DB is the source of
// truth" convention: a locked/read-only directory must not leave the
// already-uninstalled plugin's menu amendments stuck in memory because the
// caller's ReloadPlugins never ran.
func removeBuiltin(ctx context.Context, db *sql.DB, pluginID string) error {
	// DB rows and files under the per-plugin lock (ut-docs#3082).
	if err := plugins.UninstallPluginTree(ctx, db, paths.Plugins(), pluginID); err != nil {
		return fmt.Errorf("builtinlayouts: uninstall builtin layout %s: %w", pluginID, err)
	}
	return nil
}
