package data

import (
	"context"
	"database/sql"
)

// PluginScheduleRow is one persisted `schedules[]` entry of an installed
// plugin (ADR-0121 §8, ut-docs#3161; migration 068). Written only from an
// installed, Ed25519-verified manifest (PersistManifest and Rollback in
// internal/plugins); uninstall removes the rows through ON DELETE CASCADE
// on plugins(id).
type PluginScheduleRow struct {
	PluginID string
	Event    string
	EveryS   int
	JitterS  int
}

// ReplacePluginSchedules replaces pluginID's persisted schedules with rows
// (empty clears them). tx is the install/rollback transaction. The rows'
// own PluginID is ignored: every row is written for pluginID.
func (r *PluginRepo) ReplacePluginSchedules(ctx context.Context, tx *sql.Tx, pluginID string, rows []PluginScheduleRow) error {
	exec := r.executor(tx)
	if _, err := exec.ExecContext(ctx, `DELETE FROM plugin_schedules WHERE plugin_id = ?`, pluginID); err != nil {
		return pluginObs.wrap("replace_schedules", err)
	}
	for _, s := range rows {
		if _, err := exec.ExecContext(ctx,
			`INSERT INTO plugin_schedules (plugin_id, event, every_s, jitter_s) VALUES (?, ?, ?, ?)`,
			pluginID, s.Event, s.EveryS, s.JitterS); err != nil {
			return pluginObs.wrap("replace_schedules", err)
		}
	}
	return nil
}

// ListPluginSchedules returns every installed plugin's persisted schedules,
// ordered by plugin id then event. The caller (the wasm runtime's Sync)
// decides which plugins may tick: active, loaded and holding `schedule`.
func (r *PluginRepo) ListPluginSchedules(ctx context.Context) ([]PluginScheduleRow, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT plugin_id, event, every_s, jitter_s
FROM plugin_schedules
ORDER BY plugin_id, event
`)
	if err != nil {
		return nil, pluginObs.wrap("list_schedules", err)
	}
	defer rows.Close()
	var out []PluginScheduleRow
	for rows.Next() {
		var s PluginScheduleRow
		if err := rows.Scan(&s.PluginID, &s.Event, &s.EveryS, &s.JitterS); err != nil {
			return nil, pluginObs.wrap("list_schedules", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, pluginObs.wrap("list_schedules", err)
	}
	return out, nil
}
