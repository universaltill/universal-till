package plugins

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	"github.com/universaltill/universal-till/internal/data"
)

// Deprecated: InstallPlugin verifies only a SHA256 checksum — no Ed25519
// manifest-signature verification, in contradiction of this repo's "never
// run an unverified plugin" rule (universal-till/CLAUDE.md). Its last two
// production call sites (the legacy /api/plugins/upload and
// /api/plugins/marketplace/install endpoints) were removed in ut-docs#480;
// it now has no production caller (only its own package tests). Do not wire
// this to a new route — use MarketplaceInstaller.Install
// (internal/plugins/installer_marketplace.go), which does real Ed25519
// verification, instead. Full removal is tracked as a follow-up card.
//
// InstallPlugin installs a plugin from a file path with SHA256 verification
// (no signature check).
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
