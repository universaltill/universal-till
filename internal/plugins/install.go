package plugins

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/universaltill/universal-till/internal/data"
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
		fmt.Printf("warning: failed to clear plugin storage: %v\n", err)
	}

	// Record uninstall event
	if err := repo.InsertAudit(ctx, nil, "plugin_uninstall", pluginID, map[string]any{}, time.Now()); err != nil {
		fmt.Printf("warning: failed to record uninstall audit: %v\n", err)
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
		fmt.Printf("warning: failed to record trust change audit: %v\n", err)
	}

	return nil
}
