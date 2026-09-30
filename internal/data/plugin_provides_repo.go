package data

import (
	"context"
	"database/sql"
)

// ADR-0129 §2/§3 (ut-docs#3281): an installed plugin's declared `provides`
// capabilities and `markets` live in plugin_provides / plugin_markets
// (migration 053) — the plugins table stores no manifest JSON. Both are
// written only from an installed, Ed25519-verified manifest (PersistManifest
// and Rollback in internal/plugins), so there is no path from an unverified
// manifest to a lookup here. Uninstall removes them through ON DELETE
// CASCADE on plugins(id).

// ReplacePluginProvides replaces pluginID's persisted `provides` rows with
// capabilities (empty clears them). tx is the install/rollback transaction.
func (r *PluginRepo) ReplacePluginProvides(ctx context.Context, tx *sql.Tx, pluginID string, capabilities []string) error {
	return r.replacePluginValues(ctx, tx, "replace_provides",
		`DELETE FROM plugin_provides WHERE plugin_id = ?`,
		`INSERT INTO plugin_provides (plugin_id, capability) VALUES (?, ?)`,
		pluginID, capabilities)
}

// ReplacePluginMarkets replaces pluginID's persisted `markets` rows (empty
// clears them, which means every market — ADR-0129 §3).
func (r *PluginRepo) ReplacePluginMarkets(ctx context.Context, tx *sql.Tx, pluginID string, markets []string) error {
	return r.replacePluginValues(ctx, tx, "replace_markets",
		`DELETE FROM plugin_markets WHERE plugin_id = ?`,
		`INSERT INTO plugin_markets (plugin_id, market) VALUES (?, ?)`,
		pluginID, markets)
}

func (r *PluginRepo) replacePluginValues(ctx context.Context, tx *sql.Tx, op, deleteSQL, insertSQL, pluginID string, values []string) error {
	exec := r.executor(tx)
	if _, err := exec.ExecContext(ctx, deleteSQL, pluginID); err != nil {
		return pluginObs.wrap(op, err)
	}
	for _, v := range values {
		if _, err := exec.ExecContext(ctx, insertSQL, pluginID, v); err != nil {
			return pluginObs.wrap(op, err)
		}
	}
	return nil
}

// PluginsProviding returns the IDs of installed plugins whose manifest
// declares capability, sorted by ID. activeOnly=false includes disabled
// plugins: fiscal.register reads the installed provider so a disabled
// plugin's register rows (legal records) still sync, while fiscal.device
// and ai read only the active one (ADR-0129 §2). Choosing among several is
// the caller's policy, never this method's.
func (r *PluginRepo) PluginsProviding(ctx context.Context, capability string, activeOnly bool) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT p.id
FROM plugins p
JOIN plugin_provides pp ON pp.plugin_id = p.id
WHERE pp.capability = ?
  AND (? = 0 OR p.is_active = 1)
ORDER BY p.id
`, capability, activeOnly)
	if err != nil {
		return nil, pluginObs.wrap("plugins_providing", err)
	}
	return scanStrings(rows, "plugins_providing")
}

// ListPluginProvides returns pluginID's persisted capabilities, sorted —
// what the enable handler checks when only the DB rows exist.
func (r *PluginRepo) ListPluginProvides(ctx context.Context, pluginID string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT capability FROM plugin_provides WHERE plugin_id = ? ORDER BY capability`, pluginID)
	if err != nil {
		return nil, pluginObs.wrap("list_provides", err)
	}
	return scanStrings(rows, "list_provides")
}

// ListPluginMarkets returns pluginID's persisted markets, sorted. Empty
// means every market (ADR-0129 §3).
func (r *PluginRepo) ListPluginMarkets(ctx context.Context, pluginID string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT market FROM plugin_markets WHERE plugin_id = ? ORDER BY market`, pluginID)
	if err != nil {
		return nil, pluginObs.wrap("list_markets", err)
	}
	return scanStrings(rows, "list_markets")
}

// CapabilityProviderOwner returns an INSTALLED plugin other than
// excludePluginID, active or disabled, that declares capability — the
// ownership query behind ADR-0129 §2's `fiscal.*` exclusivity (at most one
// installed provider each). tx is the install/rollback transaction, nil
// from the enable handler. found=false means no other plugin provides it.
// A DB error is returned for the caller to fail CLOSED on.
func (r *PluginRepo) CapabilityProviderOwner(ctx context.Context, tx *sql.Tx, capability, excludePluginID string) (id, name string, found bool, err error) {
	err = r.executor(tx).QueryRowContext(ctx, `
SELECT p.id, COALESCE(p.name, p.id)
FROM plugins p
JOIN plugin_provides pp ON pp.plugin_id = p.id
WHERE pp.capability = ?
  AND p.id <> ?
ORDER BY p.id
LIMIT 1
`, capability, excludePluginID).Scan(&id, &name)
	if err == sql.ErrNoRows {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, pluginObs.wrap("capability_provider_owner", err)
	}
	return id, name, true, nil
}

func scanStrings(rows *sql.Rows, op string) ([]string, error) {
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, pluginObs.wrap(op, err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, pluginObs.wrap(op, err)
	}
	return out, nil
}
