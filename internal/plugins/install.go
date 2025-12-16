package plugins

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	"github.com/universaltill/universal-till/internal/data"
)

// InstallPlugin installs a plugin from a file path with SHA256 verification
func InstallPlugin(ctx context.Context, db *sql.DB, manifestPath, binaryPath string, opts InstallOptions) error {
	repo := data.NewPluginRepo(db)
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
	if err := PersistManifest(ctx, db, manifest, opts); err != nil { // TODO: move PersistManifest internals into repo in a later cleanup
		return fmt.Errorf("persist manifest: %w", err)
	}

	// 6. Record installation event in audit log
	if err := repo.InsertAudit(ctx, nil, "plugin_install", manifest.ID, map[string]any{
		"source":   opts.InstalledFromURL,
		"checksum": opts.SHA256,
		"trust":    opts.TrustLevel,
		"uploader": opts.Uploader,
	}, time.Now()); err != nil {
		// Non-fatal: log but continue
		fmt.Printf("warning: failed to record install audit: %v\n", err)
	}

	return nil
}

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

// recordInstallAudit logs plugin installation to audit_log
func recordInstallAudit(ctx context.Context, db *sql.DB, pluginID, version string, opts InstallOptions) error {
	repo := data.NewPluginRepo(db)
	return repo.InsertAudit(ctx, nil, "plugin_install", pluginID, map[string]any{
		"source":   opts.InstalledFromURL,
		"checksum": opts.SHA256,
		"trust":    opts.TrustLevel,
		"uploader": opts.Uploader,
		"version":  version,
	}, time.Now())
}

// recordUninstallAudit logs plugin uninstallation to audit_log
func recordUninstallAudit(ctx context.Context, db *sql.DB, pluginID string) error {
	repo := data.NewPluginRepo(db)
	return repo.InsertAudit(ctx, nil, "plugin_uninstall", pluginID, map[string]any{}, time.Now())
}

// recordTrustChangeAudit logs trust level changes to audit_log
func recordTrustChangeAudit(ctx context.Context, db *sql.DB, pluginID, newTrustLevel string) error {
	repo := data.NewPluginRepo(db)
	return repo.InsertAudit(ctx, nil, "plugin_trust_change", pluginID, map[string]any{
		"trust_level": newTrustLevel,
	}, time.Now())
}
