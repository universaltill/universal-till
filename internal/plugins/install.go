package plugins

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
)

// UninstallPlugin removes a plugin and its entries
func UninstallPlugin(ctx context.Context, db *sql.DB, pluginID string) error {
	repo := data.NewPluginRepo(db)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Foreign key constraints will cascade delete entries, settings, hooks, permissions
	if err := repo.DeletePlugin(ctx, tx, pluginID); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}

	// plugin_storage is namespaced KV without an FK — clear it explicitly.
	// EXCEPT the fiscal-register prefix (ADR-0072/ut-docs#1106 review finding
	// B1): this function is reachable not only from a deliberate operator
	// uninstall but also from fully automatic paths with no operator action
	// at all — sync-driven pruning of a plugin the primary no longer has
	// (pages.sync_admin.go) and a pinned-version-mismatch rollback
	// (pages.cloudsync_wire.go) both call UninstallPlugin. A legally-relevant
	// record (the §146a Abs. 4 AO till/TSE bookkeeping) must never be
	// destroyed by either, per migration 059's own "destroys nothing"
	// discipline (ADR-0042) and this product's standing "never silently
	// destroy data" principle. If the operator genuinely wants old AO
	// records purged, that is a separate, deliberate, explicit action this
	// card does not build.
	if err := repo.DeleteStorageExceptPrefix(ctx, pluginID, data.FiscalRegisterDEKeyPrefix); err != nil {
		logging.L().Warnf("failed to clear plugin storage: %v", err)
	}

	// Record uninstall event
	if err := repo.InsertAudit(ctx, nil, "plugin_uninstall", pluginID, map[string]any{}, time.Now()); err != nil {
		logging.L().Warnf("failed to record uninstall audit: %v", err)
	}

	return nil
}

// UpdatePluginTrustLevel changes the trust level of a plugin
func UpdatePluginTrustLevel(ctx context.Context, db *sql.DB, pluginID, trustLevel string) error {
	repo := data.NewPluginRepo(db)
	validLevels := map[string]bool{
		"untrusted": true,
		"verified":  true,
		"trusted":   true,
		"revoked":   true,
	}

	if !validLevels[trustLevel] {
		return fmt.Errorf("invalid trust level: %s (must be untrusted, verified, trusted, or revoked)", trustLevel)
	}

	if err := repo.UpdatePluginTrust(ctx, nil, pluginID, trustLevel); err != nil {
		return err
	}

	// Audit the trust change
	if err := repo.InsertAudit(ctx, nil, "plugin_trust_change", pluginID, fmt.Sprintf("trust_level=%s", trustLevel), time.Now()); err != nil {
		logging.L().Warnf("failed to record trust change audit: %v", err)
	}

	return nil
}

// UninstallPluginTree is the whole uninstall every caller wants: the DB rows
// (UninstallPlugin) and then the plugin's files under pluginBaseDir/pluginID
// (the live per-version dirs and the versions/ snapshots). It holds the
// per-plugin lock that serializes Rollback and StoreVersion (ut-docs#3082),
// so an uninstall that races a rollback or a snapshot write waits for it
// instead of deleting the tree under it. File removal is best-effort — the
// DB is the source of truth — so only a DB failure is returned. The lock
// wait ignores ctx; it is bounded by one rollback (a copy plus one tx).
func UninstallPluginTree(ctx context.Context, db *sql.DB, pluginBaseDir, pluginID string) error {
	// Joined under pluginBaseDir and RemoveAll'd: "." or "" would be every
	// plugin's files (ut-docs#2891 M2).
	if err := validatePluginID(pluginID); err != nil {
		return err
	}
	defer lockPluginTree(pluginBaseDir, pluginID)()

	if err := UninstallPlugin(ctx, db, pluginID); err != nil {
		return err
	}
	dir := filepath.Join(pluginBaseDir, pluginID)
	if err := os.RemoveAll(dir); err != nil {
		logging.L().Warnf("failed to remove plugin files %s: %v", dir, err)
	}
	return nil
}

// RemoveVersionDir removes one live per-version install dir
// (pluginBaseDir/pluginID/version) under the per-plugin lock, so a
// concurrent StoreVersion can't snapshot it half-deleted (ut-docs#3082).
func RemoveVersionDir(pluginBaseDir, pluginID, version string) error {
	if err := validatePluginID(pluginID); err != nil {
		return err
	}
	if err := validatePluginVersion(version); err != nil {
		return err
	}
	defer lockPluginTree(pluginBaseDir, pluginID)()
	return os.RemoveAll(filepath.Join(pluginBaseDir, pluginID, version))
}
