package plugins

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"
)

// InstallPlugin installs a plugin from a file path with SHA256 verification
func InstallPlugin(ctx context.Context, db *sql.DB, manifestPath, binaryPath string, opts InstallOptions) error {
	// 1. Verify SHA256 checksum if provided
	if opts.SHA256 != "" {
		computedHash, err := ComputeSHA256(binaryPath)
		if err != nil {
			return fmt.Errorf("compute checksum: %w", err)
		}

		if computedHash != opts.SHA256 {
			return fmt.Errorf("checksum mismatch: expected %s, got %s", opts.SHA256, computedHash)
		}
	}

	// 2. Parse manifest
	f, err := os.Open(manifestPath)
	if err != nil {
		return fmt.Errorf("open manifest: %w", err)
	}
	defer f.Close()

	manifest, err := ParseManifest(f)
	if err != nil {
		return fmt.Errorf("parse manifest: %w", err)
	}

	// 3. Compute and store actual checksum
	actualChecksum, err := ComputeSHA256(binaryPath)
	if err != nil {
		return fmt.Errorf("compute actual checksum: %w", err)
	}
	opts.SHA256 = actualChecksum

	// 4. Ensure default trust level
	if opts.TrustLevel == "" {
		opts.TrustLevel = "untrusted"
	}

	// 5. Persist manifest with provenance
	if err := PersistManifest(ctx, db, manifest, opts); err != nil {
		return fmt.Errorf("persist manifest: %w", err)
	}

	// 6. Record installation event in audit log
	if err := recordInstallAudit(ctx, db, manifest.ID, manifest.Version, opts); err != nil {
		// Non-fatal: log but continue
		fmt.Printf("warning: failed to record install audit: %v\n", err)
	}

	return nil
}

// UninstallPlugin removes a plugin and its entries
func UninstallPlugin(ctx context.Context, db *sql.DB, pluginID string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Foreign key constraints will cascade delete entries, settings, hooks, permissions
	_, err = tx.ExecContext(ctx, `DELETE FROM plugins WHERE id = ?`, pluginID)
	if err != nil {
		return fmt.Errorf("delete plugin: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}

	// Record uninstall event
	if err := recordUninstallAudit(ctx, db, pluginID); err != nil {
		fmt.Printf("warning: failed to record uninstall audit: %v\n", err)
	}

	return nil
}

// UpdatePluginTrustLevel changes the trust level of a plugin
func UpdatePluginTrustLevel(ctx context.Context, db *sql.DB, pluginID, trustLevel string) error {
	validLevels := map[string]bool{
		"untrusted": true,
		"verified":  true,
		"trusted":   true,
		"revoked":   true,
	}

	if !validLevels[trustLevel] {
		return fmt.Errorf("invalid trust level: %s (must be untrusted, verified, trusted, or revoked)", trustLevel)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.ExecContext(ctx, `
		UPDATE plugins
		SET trust_level = ?, updated_at = ?
		WHERE id = ?
	`, trustLevel, now, pluginID)
	if err != nil {
		return fmt.Errorf("update trust level: %w", err)
	}

	// Audit the trust change
	if err := recordTrustChangeAudit(ctx, db, pluginID, trustLevel); err != nil {
		fmt.Printf("warning: failed to record trust change audit: %v\n", err)
	}

	return nil
}

// recordInstallAudit logs plugin installation to audit_log
func recordInstallAudit(ctx context.Context, db *sql.DB, pluginID, version string, opts InstallOptions) error {
	now := time.Now().UTC().Format(time.RFC3339)
	details := fmt.Sprintf("source=%s, checksum=%s, trust=%s, uploader=%s",
		opts.InstalledFromURL, opts.SHA256, opts.TrustLevel, opts.Uploader)

	_, err := db.ExecContext(ctx, `
		INSERT INTO audit_log (action, entity_type, entity_id, data_json, created_at)
		VALUES ('plugin_install', 'plugin', ?, ?, ?)
	`, pluginID, details, now)
	return err
}

// recordUninstallAudit logs plugin uninstallation to audit_log
func recordUninstallAudit(ctx context.Context, db *sql.DB, pluginID string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.ExecContext(ctx, `
		INSERT INTO audit_log (action, entity_type, entity_id, created_at)
		VALUES ('plugin_uninstall', 'plugin', ?, ?)
	`, pluginID, now)
	return err
}

// recordTrustChangeAudit logs trust level changes to audit_log
func recordTrustChangeAudit(ctx context.Context, db *sql.DB, pluginID, newTrustLevel string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	details := fmt.Sprintf("trust_level=%s", newTrustLevel)

	_, err := db.ExecContext(ctx, `
		INSERT INTO audit_log (action, entity_type, entity_id, data_json, created_at)
		VALUES ('plugin_trust_change', 'plugin', ?, ?, ?)
	`, pluginID, details, now)
	return err
}
