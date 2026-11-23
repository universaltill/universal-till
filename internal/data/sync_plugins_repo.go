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
	// Enabled is the primary's plugins.is_active (ut-docs#6666). A pointer so
	// that "absent" (an older primary, or a pre-6666 cached bundle) is
	// distinguishable from false: nil = leave the replica's state alone, never
	// "off". Bundled rows (ADR-0194 §9) leave it nil: present = on, absent =
	// off.
	Enabled *bool `json:"enabled,omitempty"`
}

// PluginsBundle is the wire payload of GET /api/sync/plugins.
type PluginsBundle struct {
	Plugins []PluginSyncRow `json:"plugins"`
}

// Fingerprint identifies the bundle content (same scheme as AdminBundle:
// rows are ordered by listing_id, so equal state hashes equal).
func (b PluginsBundle) Fingerprint() string {
	// The enabled state is deliberately inside the hash (ut-docs#6666): a
	// switch on the main must make replicas re-pull.
	raw, _ := json.Marshal(b.Plugins)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:16])
}

// DumpActivePlugins reads the shop's active marketplace-installed set (every
// row also carries the plugin's enabled state, plugins.is_active; a disabled
// plugin is still listed because the filter is the install state,
// ut-docs#6666):
// plugin_install_status rows in the 'active' state (the literal matches
// plugins.InstallStateActive — a shared constant would invert the
// data→plugins dependency), joined to plugins so the version reported is
// the one actually installed, not just the installer's bookkeeping. The
// inner join also drops any stale status row whose plugin was removed
// outside the normal uninstall path. File-imported plugins never appear:
// import-from-file writes no plugin_install_status row.
func (r *SyncPluginsRepo) DumpActivePlugins(ctx context.Context) (PluginsBundle, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT s.listing_id, s.plugin_id, p.name, p.version, p.is_active
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
		var enabled int
		if err := rows.Scan(&row.ListingID, &row.PluginID, &row.PluginName, &row.Version, &enabled); err != nil {
			return PluginsBundle{}, fmt.Errorf("scan active plugin: %w", err)
		}
		on := enabled != 0
		row.Enabled = &on
		bundle.Plugins = append(bundle.Plugins, row)
	}
	if err := rows.Err(); err != nil {
		return PluginsBundle{}, fmt.Errorf("dump active plugins: %w", err)
	}
	return bundle, nil
}

// DumpActivePluginsWithSystem is DumpActivePlugins plus the ENABLED bundled
// first-party plugins among systemIDs (system trust, is_active, ADR-0194):
// they have no marketplace install record, so the main till's switch for them
// would otherwise never reach a satellite. They travel as rows with a blank
// listing id, which a satellite that predates this skips (see
// convergePluginSet) and a current one applies as "switch this on". A plugin
// that is off is simply absent: absent means off.
func (r *SyncPluginsRepo) DumpActivePluginsWithSystem(ctx context.Context, systemIDs []string) (PluginsBundle, error) {
	bundle, err := r.DumpActivePlugins(ctx)
	if err != nil {
		return PluginsBundle{}, err
	}
	for _, id := range systemIDs {
		var row PluginSyncRow
		err := r.db.QueryRowContext(ctx,
			`SELECT id, name, version FROM plugins WHERE id = ? AND is_active = 1 AND trust_level = 'system'`, id).
			Scan(&row.PluginID, &row.PluginName, &row.Version)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return PluginsBundle{}, fmt.Errorf("dump bundled plugin %s: %w", id, err)
		}
		bundle.Plugins = append(bundle.Plugins, row)
	}
	return bundle, nil
}

// InstalledPluginVersion reports whether a plugin is installed locally, at
// which version, and in which install lifecycle state — the replica-side
// half of the plugin-set diff. The state matters because a row can claim
// the right version while the plugin's FILES are gone or unloadable
// (install_state='broken', ut-docs#368 — exactly what a join snapshot's
// rows-without-bytes produce): convergePluginSet must treat that as local
// drift, not as "already converged".
func (r *SyncPluginsRepo) InstalledPluginVersion(ctx context.Context, pluginID string) (version, installState string, installed bool, err error) {
	err = r.db.QueryRowContext(ctx,
		`SELECT version, COALESCE(install_state, '') FROM plugins WHERE id = ?`, pluginID).Scan(&version, &installState)
	if err == sql.ErrNoRows {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, fmt.Errorf("installed plugin version %s: %w", pluginID, err)
	}
	return version, installState, true, nil
}
