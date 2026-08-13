package data

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// SyncPluginsRepo serves the multi-till plugin-set sync (ut-docs#460,
// ADR-0011 amendment 2026-08-08): the primary publishes its ACTIVE
// marketplace-installed plugin registry as a fingerprinted bundle; replicas
// poll it and converge by re-fetching each listing from the marketplace
// themselves (verified installs — no plugin bytes ever travel till-to-till).
//
// Deliberately registry-only: plugin binaries/rows are NOT part of the admin
// bundle (see adminTables), because rows without the verified files would
// leave a replica with phantom plugins. File-imported plugins (no
// listing_id — nothing in plugin_install_status) are outside this
// mechanism's scope by design.
type SyncPluginsRepo struct{ db *sql.DB }

func NewSyncPluginsRepo(db *sql.DB) *SyncPluginsRepo { return &SyncPluginsRepo{db: db} }

// PluginSyncRow is one marketplace-installed plugin in the primary's
// registry: which listing to install (the marketplace re-fetch key) and
// what the primary actually has (for the replica's local diff).
type PluginSyncRow struct {
	ListingID  string `json:"listing_id"`
	PluginID   string `json:"plugin_id"`
	PluginName string `json:"plugin_name"`
	Version    string `json:"version"`
}

// PluginsBundle is the wire payload of GET /api/sync/plugins.
type PluginsBundle struct {
	Plugins []PluginSyncRow `json:"plugins"`
}

// Fingerprint identifies the bundle content (same scheme as AdminBundle:
// rows are ordered by listing_id, so equal state hashes equal).
func (b PluginsBundle) Fingerprint() string {
	raw, _ := json.Marshal(b.Plugins)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:16])
}

// DumpActivePlugins reads the shop's active marketplace-installed set:
// plugin_install_status rows in the 'active' state (the literal matches
// plugins.InstallStateActive — a shared constant would invert the
// data→plugins dependency), joined to plugins so the version reported is
// the one actually installed, not just the installer's bookkeeping. The
// inner join also drops any stale status row whose plugin was removed
// outside the normal uninstall path. File-imported plugins never appear:
// import-from-file writes no plugin_install_status row.
func (r *SyncPluginsRepo) DumpActivePlugins(ctx context.Context) (PluginsBundle, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT s.listing_id, s.plugin_id, p.name, p.version
FROM plugin_install_status s
JOIN plugins p ON p.id = s.plugin_id
WHERE s.state = 'active'
  AND s.listing_id IS NOT NULL AND s.listing_id != ''
  AND s.plugin_id IS NOT NULL AND s.plugin_id != ''
ORDER BY s.listing_id`)
	if err != nil {
		return PluginsBundle{}, fmt.Errorf("dump active plugins: %w", err)
	}
	defer rows.Close()
	bundle := PluginsBundle{Plugins: []PluginSyncRow{}}
	for rows.Next() {
		var row PluginSyncRow
		if err := rows.Scan(&row.ListingID, &row.PluginID, &row.PluginName, &row.Version); err != nil {
			return PluginsBundle{}, fmt.Errorf("scan active plugin: %w", err)
		}
		bundle.Plugins = append(bundle.Plugins, row)
	}
	if err := rows.Err(); err != nil {
		return PluginsBundle{}, fmt.Errorf("dump active plugins: %w", err)
	}
	return bundle, nil
}

// InstalledPluginVersion reports whether a plugin is installed locally, at
// which version, and in which install state — the replica-side half of the
// plugin-set diff. The install state matters because a matching version is
// NOT proof the plugin is healthy: a row can survive with its files deleted
// out from under it (ut-docs#368's join-snapshot gap), which the wasm
// runtime marks install_state='broken' — and the converge loop treats that
// as drift to heal even though the version already matches the primary's.
func (r *SyncPluginsRepo) InstalledPluginVersion(ctx context.Context, pluginID string) (version, installState string, installed bool, err error) {
	err = r.db.QueryRowContext(ctx,
		`SELECT version, COALESCE(install_state, 'installed') FROM plugins WHERE id = ?`, pluginID).
		Scan(&version, &installState)
	if err == sql.ErrNoRows {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, fmt.Errorf("installed plugin version %s: %w", pluginID, err)
	}
	return version, installState, true, nil
}
