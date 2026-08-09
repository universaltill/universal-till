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
	if err := repo.DeleteStorage(ctx, pluginID); err != nil {
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
