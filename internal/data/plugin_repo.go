package data

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

type PluginRepo struct {
	db *sql.DB
}

func NewPluginRepo(db *sql.DB) *PluginRepo {
	return &PluginRepo{db: db}
}

var pluginObs = newRepoObservability("plugin")

// Plugin install lifecycle states as stored in plugins.install_state (the
// column comment in 001_init.sql lists installed|disabled|broken|installing|
// uninstalling). Only the two that code branches on across packages are
// named; 'broken' means the row is registered but the plugin's on-disk
// binary could not be loaded (ut-docs#368 — the state a join snapshot's
// row-without-bytes lands in until the sync loop re-fetches the files).
const (
	PluginStateInstalled = "installed"
	PluginStateBroken    = "broken"
)

func (r *PluginRepo) InstallPlugin(ctx context.Context, tx *sql.Tx, id string) error {
	var err error
	done := pluginObs.trace("install_plugin")
	defer func() { done(err) }()
	exec := r.executor(tx)
	_, err = exec.ExecContext(ctx, `
INSERT INTO plugins (
    id, name, version, install_state, description,
    author, website, entrypoint, runtime,
    installed_from_url, installed_sha256, is_active,
    trust_level, installed_at, updated_at
)
SELECT
    pc.id, pc.name, pc.version, 'installed', pc.description,
    pc.author, pc.website, pc.entrypoint, pc.runtime,
    pc.package_url, pc.sha256, 1,
    'trusted', ?, ?
FROM plugin_catalog pc
WHERE pc.id = ?
ON CONFLICT(id) DO UPDATE SET
    version = excluded.version,
    install_state = 'installed',
    description = excluded.description,
    author = excluded.author,
    website = excluded.website,
    entrypoint = excluded.entrypoint,
    runtime = excluded.runtime,
    installed_from_url = excluded.installed_from_url,
    installed_sha256 = excluded.installed_sha256,
    is_active = 1,
    trust_level = 'trusted',
    updated_at = excluded.updated_at
`, time.Now(), time.Now(), id)
	if err != nil {
		return pluginObs.wrapf("install_plugin", "install plugin %s", err, id)
	}
	return nil
}

// UpsertPluginManifest persists/updates plugin metadata from a manifest.
func (r *PluginRepo) UpsertPluginManifest(ctx context.Context, tx *sql.Tx, manifest ManifestRow) error {
	exec := r.executor(tx)
	_, err := exec.ExecContext(ctx, `
INSERT INTO plugins (
    id, name, version, install_state, description,
    author, website, entrypoint, runtime,
    installed_from_url, installed_sha256, is_active,
    trust_level, installed_at, updated_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    name = excluded.name,
    version = excluded.version,
    install_state = excluded.install_state,
    description = excluded.description,
    author = excluded.author,
    website = excluded.website,
    entrypoint = excluded.entrypoint,
    runtime = excluded.runtime,
    installed_from_url = excluded.installed_from_url,
    installed_sha256 = excluded.installed_sha256,
    is_active = 1,
    trust_level = excluded.trust_level,
    updated_at = excluded.updated_at
`, manifest.ID, manifest.Name, manifest.Version, manifest.InstallState, manifest.Description, manifest.Author, manifest.Website, manifest.Entrypoint, manifest.Runtime, manifest.PackageURL, manifest.SHA256, manifest.TrustLevel, manifest.InstalledAt, manifest.UpdatedAt)
	if err != nil {
		return pluginObs.wrapf("upsert_manifest", "upsert plugin %s", err, manifest.ID)
	}
	return nil
}

// DeletePlugin removes a plugin and cascades related data via FK.
func (r *PluginRepo) DeletePlugin(ctx context.Context, tx *sql.Tx, pluginID string) error {
	exec := r.executor(tx)
	if _, err := exec.ExecContext(ctx, `DELETE FROM plugins WHERE id = ?`, pluginID); err != nil {
		return pluginObs.wrap("delete_plugin", err)
	}
	return nil
}

// UpdatePluginTrust sets the trust level and updated_at.
func (r *PluginRepo) UpdatePluginTrust(ctx context.Context, tx *sql.Tx, pluginID, trustLevel string) error {
	_, err := r.executor(tx).ExecContext(ctx, `
UPDATE plugins
SET trust_level = ?, updated_at = ?
WHERE id = ?
`, trustLevel, time.Now().UTC().Format(time.RFC3339), pluginID)
	if err != nil {
		return pluginObs.wrapf("update_trust", "update trust %s", err, pluginID)
	}
	return nil
}

// InsertAudit writes an audit row for plugins.
func (r *PluginRepo) InsertAudit(ctx context.Context, tx *sql.Tx, action, pluginID string, payload any, ts time.Time) error {
	data, _ := json.Marshal(payload)
	exec := r.executor(tx)
	_, err := exec.ExecContext(ctx, `
INSERT INTO audit_log (id, actor_id, entity_type, entity_id, action, data_json, created_at)
VALUES (?, NULL, 'plugin', ?, ?, ?, ?)
`, uuid.NewString(), pluginID, action, string(data), ts.UTC().Format(time.RFC3339))
	if err != nil {
		return pluginObs.wrapf("audit", "audit %s", err, action)
	}
	return nil
}

// InsertAuditRaw allows callers to log arbitrary audit rows with a custom entity type.
func (r *PluginRepo) InsertAuditRaw(ctx context.Context, tx *sql.Tx, action, entityType, entityID string, payload any, ts time.Time) error {
	data, _ := json.Marshal(payload)
	exec := r.executor(tx)
	_, err := exec.ExecContext(ctx, `
INSERT INTO audit_log (id, actor_id, entity_type, entity_id, action, data_json, created_at)
VALUES (?, NULL, ?, ?, ?, ?, ?)
`, uuid.NewString(), entityType, entityID, action, string(data), ts.UTC().Format(time.RFC3339))
	if err != nil {
		return pluginObs.wrapf("audit_raw", "audit %s", err, action)
	}
	return nil
}

// UpdatePluginVersion updates plugin version/entrypoint/state.
func (r *PluginRepo) UpdatePluginVersion(ctx context.Context, tx *sql.Tx, pluginID, version, entrypoint string, installState string) error {
	_, err := r.executor(tx).ExecContext(ctx, `
UPDATE plugins 
SET version = ?, entrypoint = ?, updated_at = ?, install_state = ?
WHERE id = ?
`, version, entrypoint, time.Now().UTC().Format(time.RFC3339), installState, pluginID)
	if err != nil {
		return pluginObs.wrapf("update_version", "update version %s", err, pluginID)
	}
	return nil
}

// ReplacePluginEntries replaces entries for a plugin (used on rollback/install).
func (r *PluginRepo) ReplacePluginEntries(ctx context.Context, tx *sql.Tx, pluginID string, entries []PluginEntryRow) error {
	exec := r.executor(tx)
	if _, err := exec.ExecContext(ctx, `DELETE FROM plugin_entries WHERE plugin_id = ?`, pluginID); err != nil {
		return pluginObs.wrap("delete_entries", err)
	}
	if len(entries) == 0 {
		return nil
	}
	stmt, err := exec.PrepareContext(ctx, `
INSERT INTO plugin_entries (
    id, plugin_id, type, key, label, icon_path, sort_order, is_active,
    parent_page_key, menu_group, route, target_action, trigger_event,
    config_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?)
`)
	if err != nil {
		return pluginObs.wrap("prepare_entries", err)
	}
	defer stmt.Close()
	now := time.Now().UTC().Format(time.RFC3339)
	for _, e := range entries {
		if e.ID == "" {
			e.ID = uuid.NewString()
		}
		if _, err := stmt.ExecContext(ctx, e.ID, pluginID, e.Type, e.Key, e.Label, e.IconPath, e.SortOrder, e.ParentPageKey, e.MenuGroup, e.Route, e.TargetAction, e.TriggerEvent, e.ConfigJSON, now, now); err != nil {
			return pluginObs.wrap("insert_entry", err)
		}
	}
	return nil
}

// InsertPluginSettings upserts plugin settings.
// ReconcilePluginSettings aligns a plugin's settings rows with its manifest
// on install/upgrade WITHOUT losing operator-configured values:
//   - a declared key with no row gets its default inserted;
//   - a declared key whose row sits in a different scope keeps its value but
//     moves to the manifest's scope (e.g. a per-till reader id that used to
//     be declared global);
//   - duplicate rows for one key collapse to the best candidate — a value
//     differing from the default beats the default, then newer beats older.
//     (Dupes came from the previous upsert: its conflict target included
//     scope_id, which is NULL here, and SQLite treats NULLs as distinct, so
//     every upgrade inserted another default row.)
//   - keys the manifest no longer declares are removed.
func (r *PluginRepo) ReconcilePluginSettings(ctx context.Context, tx *sql.Tx, pluginID string, declared []PluginSettingRow) error {
	exec := r.executor(tx)
	rows, err := exec.QueryContext(ctx, `
SELECT id, key, value_json, scope, updated_at FROM plugin_settings WHERE plugin_id = ?`, pluginID)
	if err != nil {
		return pluginObs.wrap("reconcile_settings", err)
	}
	type existing struct{ id, key, valueJSON, scope, updatedAt string }
	var have []existing
	for rows.Next() {
		var e existing
		if err := rows.Scan(&e.id, &e.key, &e.valueJSON, &e.scope, &e.updatedAt); err != nil {
			rows.Close()
			return pluginObs.wrap("reconcile_settings", err)
		}
		have = append(have, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return pluginObs.wrap("reconcile_settings", err)
	}

	declaredByKey := make(map[string]PluginSettingRow, len(declared))
	for _, d := range declared {
		declaredByKey[d.Key] = d
	}
	byKey := map[string][]existing{}
	for _, e := range have {
		byKey[e.key] = append(byKey[e.key], e)
	}

	del := func(id string) error {
		_, err := exec.ExecContext(ctx, `DELETE FROM plugin_settings WHERE id = ?`, id)
		return err
	}
	for key, es := range byKey {
		d, ok := declaredByKey[key]
		if !ok {
			for _, e := range es {
				if err := del(e.id); err != nil {
					return pluginObs.wrap("reconcile_settings", err)
				}
			}
			continue
		}
		// Best candidate: a configured (non-default) value wins over the
		// default; ties break to the latest write. Both stored timestamp
		// formats (RFC3339 and SQLite datetime) compare correctly as strings
		// on the date part.
		best := es[0]
		for _, e := range es[1:] {
			bc, ec := best.valueJSON != d.ValueJSON, e.valueJSON != d.ValueJSON
			if (ec && !bc) || (ec == bc && e.updatedAt > best.updatedAt) {
				best = e
			}
		}
		for _, e := range es {
			if e.id != best.id {
				if err := del(e.id); err != nil {
					return pluginObs.wrap("reconcile_settings", err)
				}
			}
		}
		if best.scope != d.Scope {
			if _, err := exec.ExecContext(ctx, `
UPDATE plugin_settings SET scope = ?, scope_id = NULL, updated_at = ? WHERE id = ?`,
				d.Scope, time.Now().UTC().Format(time.RFC3339), best.id); err != nil {
				return pluginObs.wrap("reconcile_settings", err)
			}
		}
	}

	for _, d := range declared {
		if _, ok := byKey[d.Key]; ok {
			continue
		}
		if d.ID == "" {
			d.ID = uuid.NewString()
		}
		if _, err := exec.ExecContext(ctx, `
INSERT INTO plugin_settings (id, plugin_id, key, value_json, scope, scope_id, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
			d.ID, pluginID, d.Key, d.ValueJSON, d.Scope, d.ScopeID,
			d.UpdatedAt.UTC().Format(time.RFC3339)); err != nil {
			return pluginObs.wrap("reconcile_settings", err)
		}
	}
	return nil
}

// InsertPluginHooks upserts plugin hooks.
func (r *PluginRepo) InsertPluginHooks(ctx context.Context, tx *sql.Tx, hooks []PluginHookRow) error {
	if len(hooks) == 0 {
		return nil
	}
	exec := r.executor(tx)
	stmt, err := exec.PrepareContext(ctx, `
INSERT INTO plugin_hooks (
	id, plugin_id, event, action, priority, is_active, config_json
) VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(plugin_id, event, action) DO UPDATE SET
	priority = excluded.priority,
	config_json = excluded.config_json
`)
	if err != nil {
		return pluginObs.wrap("prepare_hooks", err)
	}
	defer stmt.Close()
	for _, h := range hooks {
		if h.ID == "" {
			h.ID = uuid.NewString()
		}
		active := 0
		if h.IsActive {
			active = 1
		}
		if _, err := stmt.ExecContext(ctx, h.ID, h.PluginID, h.Event, h.Action, h.Priority, active, h.ConfigJSON); err != nil {
			return pluginObs.wrap("insert_hook", err)
		}
	}
	return nil
}

// InsertPluginPermissions ensures permission rows exist (default not granted).
func (r *PluginRepo) InsertPluginPermissions(ctx context.Context, tx *sql.Tx, pluginID string, permissions []string) error {
	if len(permissions) == 0 {
		return nil
	}
	exec := r.executor(tx)
	stmt, err := exec.PrepareContext(ctx, `
INSERT INTO plugin_permissions (
	id, plugin_id, permission, granted
) VALUES (?, ?, ?, 0)
ON CONFLICT(plugin_id, permission) DO NOTHING
`)
	if err != nil {
		return pluginObs.wrap("prepare_permissions", err)
	}
	defer stmt.Close()
	for _, perm := range permissions {
		if perm == "" {
			continue
		}
		if _, err := stmt.ExecContext(ctx, uuid.NewString(), pluginID, perm); err != nil {
			return pluginObs.wrap("insert_permission", err)
		}
	}
	return nil
}

// SetPluginActive flags a plugin as active/inactive.
func (r *PluginRepo) SetPluginActive(ctx context.Context, tx *sql.Tx, pluginID string, active bool) error {
	val := 0
	if active {
		val = 1
	}
	_, err := r.executor(tx).ExecContext(ctx, `UPDATE plugins SET is_active = ?, updated_at = ? WHERE id = ?`, val, time.Now().UTC().Format(time.RFC3339), pluginID)
	if err != nil {
		return pluginObs.wrapf("set_active", "set active %s", err, pluginID)
	}
	return nil
}

// GetPlugin returns plugin metadata optionally filtered by version.
func (r *PluginRepo) GetPlugin(ctx context.Context, pluginID, version string) (PluginInfoRow, bool, error) {
	row := PluginInfoRow{}
	args := []any{pluginID}
	query := `
SELECT id, COALESCE(version, ''), COALESCE(entrypoint, ''), COALESCE(runtime, ''), COALESCE(install_state, ''), is_active
FROM plugins
WHERE id = ?
`
	if version != "" {
		query += " AND version = ?"
		args = append(args, version)
	}
	query += " LIMIT 1"
	var active int
	err := r.db.QueryRowContext(ctx, query, args...).Scan(&row.ID, &row.Version, &row.Entrypoint, &row.Runtime, &row.InstallState, &active)
	if err == sql.ErrNoRows {
		return row, false, nil
	}
	if err != nil {
		return row, false, pluginObs.wrap("get_plugin", err)
	}
	row.IsActive = active == 1
	return row, true, nil
}

// GetActivePluginVersion fetches the active version for a plugin.
func (r *PluginRepo) GetActivePluginVersion(ctx context.Context, pluginID string) (string, bool, error) {
	var version string
	err := r.db.QueryRowContext(ctx, `
SELECT version FROM plugins WHERE id = ? AND is_active = 1 LIMIT 1
`, pluginID).Scan(&version)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, pluginObs.wrap("get_active_version", err)
	}
	return version, true, nil
}

// UpdatePluginInstallState flips ONLY the install lifecycle state of one
// plugin (is_active and version are untouched, unlike SetPluginState).
// WasmRuntime.Sync uses it to mark a registered-but-unloadable plugin
// 'broken' and to flip it back to 'installed' on a later successful load
// (ut-docs#368) — both directions must be visible in the row, not just in
// the log.
func (r *PluginRepo) UpdatePluginInstallState(ctx context.Context, pluginID, state string) error {
	_, err := r.db.ExecContext(ctx, `
UPDATE plugins
SET install_state = ?, updated_at = ?
WHERE id = ?
`, state, time.Now().UTC().Format(time.RFC3339), pluginID)
	if err != nil {
		return pluginObs.wrapf("update_install_state", "update install state %s", err, pluginID)
	}
	return nil
}

// HasBrokenActivePluginForEvent reports whether any ACTIVE plugin whose
// manifest-registered hook events (plugin_hooks — written at install time,
// independent of whether the plugin is currently loaded) include event
// currently sits in install_state='broken'. This is the tax fail-closed
// check (ut-docs#368): a broken tax plugin cannot subscribe to the event
// bus, so "no subscribers" alone cannot distinguish "no tax plugin was ever
// installed" from "the plugin that owns this event is broken right now" —
// this manifest-registration query can.
func (r *PluginRepo) HasBrokenActivePluginForEvent(ctx context.Context, event string) (bool, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM plugins p
JOIN plugin_hooks h ON h.plugin_id = p.id
WHERE p.is_active = 1
  AND p.install_state = 'broken'
  AND h.is_active = 1
  AND h.event = ?
`, event).Scan(&n)
	if err != nil {
		return false, pluginObs.wrap("has_broken_for_event", err)
	}
	return n > 0, nil
}

// ActiveHookOwner returns the id and display name of an ACTIVE plugin other
// than excludePluginID currently holding an active hook for event — the
// exclusivity check for an ADR-0041 `exclusive` extension point
// (fiscal.sign.ask today, ut-docs#675): setPluginActiveHandler refuses to
// enable a second answerer while this owner is active, and PersistManifest
// refuses an install/update whose manifest declares the hook (review of
// ut-docs#675, B2 — pass the install transaction as tx so the check sees a
// consistent snapshot; nil outside one). found=false means the point is
// unowned (or owned only by the excluded plugin itself, e.g. a re-enable
// or a self-update).
func (r *PluginRepo) ActiveHookOwner(ctx context.Context, tx *sql.Tx, event, excludePluginID string) (id, name string, found bool, err error) {
	err = r.executor(tx).QueryRowContext(ctx, `
SELECT p.id, COALESCE(p.name, p.id)
FROM plugins p
JOIN plugin_hooks h ON h.plugin_id = p.id
WHERE p.is_active = 1
  AND h.is_active = 1
  AND h.event = ?
  AND p.id <> ?
LIMIT 1
`, event, excludePluginID).Scan(&id, &name)
	if err == sql.ErrNoRows {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, pluginObs.wrap("active_hook_owner", err)
	}
	return id, name, true, nil
}

// SetPluginState toggles active flag and install state for a specific version.
func (r *PluginRepo) SetPluginState(ctx context.Context, pluginID, version, installState string, active bool) error {
	val := 0
	if active {
		val = 1
	}
	_, err := r.db.ExecContext(ctx, `
UPDATE plugins
SET is_active = ?, install_state = ?, updated_at = ?
WHERE id = ? AND version = ?
`, val, installState, time.Now().UTC().Format(time.RFC3339), pluginID, version)
	if err != nil {
		return pluginObs.wrapf("set_state", "set state %s", err, pluginID)
	}
	return nil
}

// ListRevokedPlugins returns plugins marked as revoked.
func (r *PluginRepo) ListRevokedPlugins(ctx context.Context) ([]PluginInfoRow, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, COALESCE(version, ''), COALESCE(entrypoint, ''), COALESCE(runtime, ''), install_state, is_active
FROM plugins
WHERE install_state = 'revoked'
`)
	if err != nil {
		return nil, pluginObs.wrap("list_revoked", err)
	}
	defer rows.Close()
	var res []PluginInfoRow
	for rows.Next() {
		var row PluginInfoRow
		var active int
		if err := rows.Scan(&row.ID, &row.Version, &row.Entrypoint, &row.Runtime, &row.InstallState, &active); err != nil {
			return nil, pluginObs.wrap("list_revoked", err)
		}
		row.IsActive = active == 1
		res = append(res, row)
	}
	return res, rows.Err()
}

// ListAutoStartPlugins returns active installed plugins for auto-start.
func (r *PluginRepo) ListAutoStartPlugins(ctx context.Context) ([]AutoStartRow, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, entrypoint, runtime
FROM plugins
WHERE is_active = 1 AND install_state = 'installed'
ORDER BY id
`)
	if err != nil {
		return nil, pluginObs.wrap("list_autostart", err)
	}
	defer rows.Close()
	var res []AutoStartRow
	for rows.Next() {
		var row AutoStartRow
		if err := rows.Scan(&row.ID, &row.Entrypoint, &row.Runtime); err != nil {
			return nil, pluginObs.wrap("list_autostart", err)
		}
		res = append(res, row)
	}
	return res, rows.Err()
}

// HasActiveHook checks if a plugin has an active hook for an event.
func (r *PluginRepo) HasActiveHook(ctx context.Context, pluginID, event string) (bool, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM plugin_hooks
WHERE plugin_id = ? AND event = ? AND is_active = 1
`, pluginID, event).Scan(&count)
	if err != nil {
		return false, pluginObs.wrap("has_active_hook", err)
	}
	return count > 0, nil
}

// Permission helpers

func (r *PluginRepo) CheckPermission(ctx context.Context, pluginID, permission string) (granted bool, exists bool, err error) {
	var grantedInt int
	err = r.executor(nil).QueryRowContext(ctx, `
SELECT granted
FROM plugin_permissions
WHERE plugin_id = ? AND permission = ?
`, pluginID, permission).Scan(&grantedInt)
	if err == sql.ErrNoRows {
		return false, false, nil
	}
	if err != nil {
		return false, false, pluginObs.wrap("check_permission", err)
	}
	return grantedInt == 1, true, nil
}

func (r *PluginRepo) SetPermission(ctx context.Context, pluginID, permission string, granted bool) error {
	val := 0
	if granted {
		val = 1
	}
	res, err := r.executor(nil).ExecContext(ctx, `
UPDATE plugin_permissions
SET granted = ?
WHERE plugin_id = ? AND permission = ?
`, val, pluginID, permission)
	if err != nil {
		return pluginObs.wrap("set_permission", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("permission %s not found for plugin %s", permission, pluginID)
	}
	return nil
}

func (r *PluginRepo) ListPermissions(ctx context.Context, pluginID string) ([]PermissionRow, error) {
	rows, err := r.executor(nil).QueryContext(ctx, `
SELECT permission, granted
FROM plugin_permissions
WHERE plugin_id = ?
ORDER BY permission
`, pluginID)
	if err != nil {
		return nil, pluginObs.wrap("list_permissions", err)
	}
	defer rows.Close()
	var res []PermissionRow
	for rows.Next() {
		var p PermissionRow
		if err := rows.Scan(&p.Permission, &p.Granted); err != nil {
			return nil, pluginObs.wrap("list_permissions", err)
		}
		res = append(res, p)
	}
	return res, rows.Err()
}

// ListPluginSettings returns a plugin's global-scope settings (the
// manager settings editor + host feature configs read these).
func (r *PluginRepo) ListPluginSettings(ctx context.Context, pluginID string) ([]PluginSettingRow, error) {
	rows, err := r.executor(nil).QueryContext(ctx, `
SELECT id, plugin_id, key, value_json, scope, scope_id, updated_at
FROM plugin_settings WHERE plugin_id = ? AND scope IN ('global', 'register') ORDER BY key`, pluginID)
	if err != nil {
		return nil, pluginObs.wrap("list_settings", err)
	}
	defer rows.Close()
	var out []PluginSettingRow
	for rows.Next() {
		var s PluginSettingRow
		var updated string // stored as SQLite datetime TEXT
		if err := rows.Scan(&s.ID, &s.PluginID, &s.Key, &s.ValueJSON, &s.Scope, &s.ScopeID, &updated); err != nil {
			return nil, pluginObs.wrap("scan_setting", err)
		}
		s.UpdatedAt, _ = time.Parse("2006-01-02 15:04:05", updated)
		out = append(out, s)
	}
	return out, rows.Err()
}

// GetPluginSetting returns one setting's raw value_json for a plugin,
// preferring the most specific scope (register beats global) so a per-till
// value shadows a shop-wide one. found is false when the key is not set.
// Backs the settings_get WASM host function (configurable connector
// plugins, ADR-0014).
func (r *PluginRepo) GetPluginSetting(ctx context.Context, pluginID, key string) (string, bool, error) {
	var valueJSON string
	err := r.executor(nil).QueryRowContext(ctx, `
SELECT value_json FROM plugin_settings WHERE plugin_id = ? AND key = ?
ORDER BY CASE scope WHEN 'register' THEN 0 WHEN 'user' THEN 1 ELSE 2 END LIMIT 1`,
		pluginID, key).Scan(&valueJSON)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, pluginObs.wrap("get_setting", err)
	}
	return valueJSON, true, nil
}

// UpsertPluginSetting sets one global-scope plugin setting.
func (r *PluginRepo) UpsertPluginSetting(ctx context.Context, pluginID, key, valueJSON string) error {
	return r.UpsertPluginSettingScoped(ctx, pluginID, key, valueJSON, "global")
}

// UpsertPluginSettingScoped sets one plugin setting in the given scope
// (global|register|user). Register-scoped rows never travel in LAN sync —
// they are this till's own (scope_id stays NULL: one till, one register).
// Update-then-insert rather than ON CONFLICT: the unique index includes
// scope_id, which is NULL here, and SQLite treats NULLs as distinct.
func (r *PluginRepo) UpsertPluginSettingScoped(ctx context.Context, pluginID, key, valueJSON, scope string) error {
	res, err := r.executor(nil).ExecContext(ctx, `
UPDATE plugin_settings SET value_json = ?, updated_at = datetime('now')
WHERE plugin_id = ? AND key = ? AND scope = ?`,
		valueJSON, pluginID, key, scope)
	if err != nil {
		return pluginObs.wrap("upsert_setting", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	_, err = r.executor(nil).ExecContext(ctx, `
INSERT INTO plugin_settings (id, plugin_id, key, value_json, scope)
VALUES (?, ?, ?, ?, ?)`,
		uuid.NewString(), pluginID, key, valueJSON, scope)
	if err != nil {
		return pluginObs.wrap("upsert_setting", err)
	}
	return nil
}

// MergeAdditiveJSONMapSetting atomically merges newEntries into a JSON
// object (string key -> int value) stored as one global-scope plugin
// setting, adding only keys not already present — an existing entry (e.g. a
// merchant's hand-set override) always wins over a newly discovered one.
// Runs the read, merge and write inside a single transaction: the DSN's
// _txlock=immediate (ut-docs#311) makes BeginTx take the write lock at
// BEGIN, so a second concurrent caller cannot start its own read until the
// first has committed its write — closing the lost-update race a separate
// GetPluginSetting + UpsertPluginSetting pair leaves open, where both calls
// can read the same starting value and the second write clobbers the
// first's additions (ut-docs#532).
//
// If the existing value isn't valid JSON, it is left completely untouched
// and an error is returned — a hand-edited value that fails to parse must
// never be silently overwritten by whatever this merge would have written.
//
// Read/write scope note (found in ut-docs#532's review, pre-existing and
// preserved rather than introduced by this method): the read prefers the
// most specific scope present (register beats user beats global — same
// preference GetPluginSetting itself uses), but the write always targets
// scope='global', matching UpsertPluginSetting/UpsertPluginSettingScoped's
// own global-only default. On a key that only ever has a global-scope row
// (true today for ut-plugin-tax-de's takeaway_rate_overrides) this is
// inert; a future caller with a register/user-scoped row on the same key
// would see the merge read that row but write the result to a *different*
// global row, silently diverging from what GetPluginSetting reports back.
// Tracked as a follow-up rather than fixed here, to keep this fix scoped
// to the atomicity bug it was opened for.
//
// Owns its own transaction (BeginTx, not an injectable tx like
// InstallPlugin's executor(tx) pattern) — matches settings_repo.go's
// GetOrCreate precedent. A future caller invoking this while already
// holding a write transaction on the same DB will block for
// busy_timeout(5000ms) and then fail with SQLITE_BUSY rather than nest.
func (r *PluginRepo) MergeAdditiveJSONMapSetting(ctx context.Context, pluginID, key string, newEntries map[string]int) (added int, err error) {
	done := pluginObs.trace("merge_additive_json_map_setting")
	defer func() { done(err) }()

	if len(newEntries) == 0 {
		return 0, nil
	}

	tx, txErr := r.db.BeginTx(ctx, nil)
	if txErr != nil {
		err = pluginObs.wrap("merge_additive_json_map_setting", txErr)
		return 0, err
	}
	defer tx.Rollback()

	existing := map[string]int{}
	var raw string
	scanErr := tx.QueryRowContext(ctx, `
SELECT value_json FROM plugin_settings WHERE plugin_id = ? AND key = ?
ORDER BY CASE scope WHEN 'register' THEN 0 WHEN 'user' THEN 1 ELSE 2 END LIMIT 1`,
		pluginID, key).Scan(&raw)
	switch {
	case scanErr != nil && !errors.Is(scanErr, sql.ErrNoRows):
		err = pluginObs.wrap("merge_additive_json_map_setting", scanErr)
		return 0, err
	case scanErr == nil && strings.TrimSpace(raw) != "":
		if jsonErr := json.Unmarshal([]byte(raw), &existing); jsonErr != nil {
			err = fmt.Errorf("existing value for %s/%s is not valid JSON: %w", pluginID, key, jsonErr)
			return 0, err
		}
	}

	for id, v := range newEntries {
		if _, ok := existing[id]; ok {
			continue
		}
		existing[id] = v
		added++
	}
	if added == 0 {
		return 0, nil
	}

	merged, marshalErr := json.Marshal(existing)
	if marshalErr != nil {
		err = fmt.Errorf("marshal merged value for %s/%s: %w", pluginID, key, marshalErr)
		return 0, err
	}

	res, execErr := tx.ExecContext(ctx, `
UPDATE plugin_settings SET value_json = ?, updated_at = datetime('now')
WHERE plugin_id = ? AND key = ? AND scope = 'global'`, string(merged), pluginID, key)
	if execErr != nil {
		err = pluginObs.wrap("merge_additive_json_map_setting", execErr)
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, insErr := tx.ExecContext(ctx, `
INSERT INTO plugin_settings (id, plugin_id, key, value_json, scope)
VALUES (?, ?, ?, ?, 'global')`, uuid.NewString(), pluginID, key, string(merged)); insErr != nil {
			err = pluginObs.wrap("merge_additive_json_map_setting", insErr)
			return 0, err
		}
	}
	if commitErr := tx.Commit(); commitErr != nil {
		err = pluginObs.wrap("merge_additive_json_map_setting", commitErr)
		return 0, err
	}
	return added, nil
}

// PluginActive reports whether a plugin is installed and enabled.
func (r *PluginRepo) PluginActive(ctx context.Context, pluginID string) (bool, error) {
	var n int
	err := r.executor(nil).QueryRowContext(ctx,
		`SELECT COUNT(*) FROM plugins WHERE id = ? AND is_active = 1`, pluginID).Scan(&n)
	if err != nil {
		return false, pluginObs.wrap("plugin_active", err)
	}
	return n > 0, nil
}

// Plugin KV storage (wasm `storage` host function). Caps keep a plugin from
// bloating a Pi's SD card; the host function surfaces violations as -4.
const (
	StorageMaxKeyBytes   = 128
	StorageMaxValueBytes = 64 << 10
	StorageMaxKeys       = 1024
)

var (
	ErrStorageNotFound = errors.New("plugin storage: key not found")
	ErrStorageTooLarge = errors.New("plugin storage: key/value over size cap or key quota reached")
)

// StorageGet reads one value from the plugin's namespace.
func (r *PluginRepo) StorageGet(ctx context.Context, pluginID, key string) ([]byte, error) {
	var v []byte
	err := r.executor(nil).QueryRowContext(ctx,
		`SELECT value FROM plugin_storage WHERE plugin_id = ? AND key = ?`,
		pluginID, key).Scan(&v)
	if err == sql.ErrNoRows {
		return nil, ErrStorageNotFound
	}
	if err != nil {
		return nil, pluginObs.wrap("storage_get", err)
	}
	return v, nil
}

// StorageSet upserts one value in the plugin's namespace, enforcing the caps.
func (r *PluginRepo) StorageSet(ctx context.Context, pluginID, key string, value []byte) error {
	if len(key) == 0 || len(key) > StorageMaxKeyBytes || len(value) > StorageMaxValueBytes {
		return ErrStorageTooLarge
	}
	var n int
	if err := r.executor(nil).QueryRowContext(ctx,
		`SELECT COUNT(*) FROM plugin_storage WHERE plugin_id = ? AND key != ?`,
		pluginID, key).Scan(&n); err != nil {
		return pluginObs.wrap("storage_set", err)
	}
	if n >= StorageMaxKeys {
		return ErrStorageTooLarge
	}
	_, err := r.executor(nil).ExecContext(ctx, `
INSERT INTO plugin_storage (plugin_id, key, value, updated_at)
VALUES (?, ?, ?, datetime('now'))
ON CONFLICT (plugin_id, key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		pluginID, key, value)
	if err != nil {
		return pluginObs.wrap("storage_set", err)
	}
	return nil
}

// DeleteStorage clears a plugin's namespace (uninstall housekeeping).
func (r *PluginRepo) DeleteStorage(ctx context.Context, pluginID string) error {
	_, err := r.executor(nil).ExecContext(ctx,
		`DELETE FROM plugin_storage WHERE plugin_id = ?`, pluginID)
	if err != nil {
		return pluginObs.wrap("storage_delete", err)
	}
	return nil
}

// Types for repo inputs
type ManifestRow struct {
	ID           string
	Name         string
	Version      string
	InstallState string
	Description  string
	Author       string
	Website      string
	Entrypoint   string
	Runtime      string
	PackageURL   string
	SHA256       string
	TrustLevel   string
	InstalledAt  time.Time
	UpdatedAt    time.Time
}

type PluginEntryRow struct {
	ID            string
	Type          string
	Key           string
	Label         string
	IconPath      string
	SortOrder     int
	ParentPageKey string
	MenuGroup     string
	Route         string
	TargetAction  string
	TriggerEvent  string
	ConfigJSON    string
}

type PermissionRow struct {
	Permission string
	Granted    bool
}

// InstalledPluginRow represents installed plugin metadata.
type InstalledPluginRow struct {
	ID         string
	Name       string
	Version    string
	Author     string
	Runtime    string
	Entrypoint string
	IsActive   bool
	// InstallState is the plugins.install_state lifecycle value —
	// WasmRuntime.Sync keys its broken/heal transitions on it (ut-docs#368).
	InstallState string
}

// MenuEntryRow represents a plugin menu entry with aggregated permissions.
type MenuEntryRow struct {
	PluginID            string
	Key                 string
	Route               string
	Label               string
	MenuGroup           string
	RequiredPermissions sql.NullString
	GrantedFlags        sql.NullString
}

// ReceiptTemplateRow represents receipt template metadata stored in plugin entries.
type ReceiptTemplateRow struct {
	PluginID      string
	PluginName    string
	PluginVersion string
	EntryKey      string
	SortOrder     int
	ConfigJSON    string
}

// PluginInfoRow represents plugin state/metadata for control plane operations.
type PluginInfoRow struct {
	ID           string
	Version      string
	Entrypoint   string
	Runtime      string
	InstallState string
	IsActive     bool
}

// AutoStartRow represents plugins eligible for auto start.
type AutoStartRow struct {
	ID         string
	Entrypoint string
	Runtime    string
}

// CatalogUpsertRow is the input for upserting a marketplace catalog entry.
type CatalogUpsertRow struct {
	ID            string
	Version       string
	Name          string
	Description   string
	Author        string
	Website       string
	Runtime       string
	Entrypoint    string
	PackageURL    string
	SHA256        string
	Signature     string
	MinPOSVersion string
	PublishedAt   string
}

// EnsureCatalogEntry inserts a plugin_catalog row if one does not already
// exist for (id, version). plugins(id,version) has a foreign key onto
// plugin_catalog, so locally imported plugins (no marketplace catalog row)
// need one before they can be persisted. Existing rows (e.g. written by the
// marketplace installer) are left untouched.
func (r *PluginRepo) EnsureCatalogEntry(ctx context.Context, tx *sql.Tx, in CatalogUpsertRow) error {
	exec := r.executor(tx)
	_, err := exec.ExecContext(ctx, `
INSERT INTO plugin_catalog (
	id, version, name, description, author, website, repository_url,
	runtime, entrypoint, package_url, sha256, signature, size_bytes,
	min_pos_version, max_pos_version, api_version, tags_json, capabilities_json,
	published_at, is_deprecated
) VALUES (?, ?, ?, ?, ?, ?, '', ?, ?, ?, ?, ?, 0, ?, '', '1.0.0', '', '', ?, 0)
ON CONFLICT(id, version) DO NOTHING
`, in.ID, in.Version, in.Name, in.Description, in.Author, in.Website,
		in.Runtime, in.Entrypoint, in.PackageURL, in.SHA256, in.Signature,
		in.MinPOSVersion, in.PublishedAt)
	if err != nil {
		return pluginObs.wrapf("ensure_catalog_entry", "ensure catalog entry %s@%s", err, in.ID, in.Version)
	}
	return nil
}

// UpsertCatalogEntry inserts or updates a plugin_catalog entry from a
// marketplace install. Keeping the SQL here preserves the repository pattern
// (data access lives only in internal/data).
func (r *PluginRepo) UpsertCatalogEntry(ctx context.Context, in CatalogUpsertRow) error {
	_, err := r.db.ExecContext(ctx, `
INSERT INTO plugin_catalog (
	id, version, name, description, author, website, repository_url,
	runtime, entrypoint, package_url, sha256, signature, size_bytes,
	min_pos_version, max_pos_version, api_version, tags_json, capabilities_json,
	published_at, is_deprecated
) VALUES (?, ?, ?, ?, ?, ?, '', ?, ?, ?, ?, ?, 0, ?, '', '1.0.0', '', '', ?, 0)
ON CONFLICT(id, version) DO UPDATE SET
	name = excluded.name,
	description = excluded.description,
	author = excluded.author,
	website = excluded.website,
	runtime = excluded.runtime,
	entrypoint = excluded.entrypoint,
	package_url = excluded.package_url,
	sha256 = excluded.sha256,
	signature = excluded.signature,
	min_pos_version = excluded.min_pos_version,
	published_at = excluded.published_at,
	is_deprecated = 0
`, in.ID, in.Version, in.Name, in.Description, in.Author, in.Website, in.Runtime, in.Entrypoint, in.PackageURL, in.SHA256, in.Signature, in.MinPOSVersion, in.PublishedAt)
	return pluginObs.wrap("upsert_catalog_entry", err)
}

// CatalogRow represents a catalog entry row from plugin_catalog.
type CatalogRow struct {
	ID          string
	Version     string
	Name        string
	Description string
	Runtime     string
	Entrypoint  string
	PackageURL  string
	SHA256      string
	Author      string
	Website     string
	TagsJSON    string
}

// PluginSettingRow represents a plugin setting row.
type PluginSettingRow struct {
	ID        string
	PluginID  string
	Key       string
	ValueJSON string
	Scope     string
	ScopeID   sql.NullString
	UpdatedAt time.Time
}

// PluginHookRow represents a plugin hook row.
type PluginHookRow struct {
	ID         string
	PluginID   string
	Event      string
	Action     string
	Priority   int
	IsActive   bool
	ConfigJSON string
}

// ManagedPluginRow is a plugin row for the management page — includes
// disabled plugins (unlike ListInstalledPlugins) so they can be re-enabled.
type ManagedPluginRow struct {
	ID           string
	Name         string
	Version      string
	IsActive     bool
	TrustLevel   string
	InstallState string
}

// ListManagedPlugins returns every plugin row regardless of active state.
func (r *PluginRepo) ListManagedPlugins(ctx context.Context) ([]ManagedPluginRow, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, name, version, is_active, trust_level, install_state
FROM plugins
ORDER BY name COLLATE NOCASE
`)
	if err != nil {
		return nil, pluginObs.wrap("list_managed", err)
	}
	defer rows.Close()
	var res []ManagedPluginRow
	for rows.Next() {
		var p ManagedPluginRow
		if err := rows.Scan(&p.ID, &p.Name, &p.Version, &p.IsActive, &p.TrustLevel, &p.InstallState); err != nil {
			return nil, pluginObs.wrap("list_managed", err)
		}
		res = append(res, p)
	}
	return res, rows.Err()
}

func (r *PluginRepo) ListInstalledPlugins(ctx context.Context) ([]InstalledPluginRow, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, name, version, COALESCE(author, ''), COALESCE(runtime, 'go'), COALESCE(entrypoint, ''), is_active, COALESCE(install_state, '')
FROM plugins
WHERE is_active = 1
`)
	if err != nil {
		return nil, pluginObs.wrap("list_installed", err)
	}
	defer rows.Close()
	var res []InstalledPluginRow
	for rows.Next() {
		var p InstalledPluginRow
		if err := rows.Scan(&p.ID, &p.Name, &p.Version, &p.Author, &p.Runtime, &p.Entrypoint, &p.IsActive, &p.InstallState); err != nil {
			return nil, pluginObs.wrap("list_installed", err)
		}
		res = append(res, p)
	}
	return res, rows.Err()
}

func (r *PluginRepo) ListMenuEntries(ctx context.Context) ([]MenuEntryRow, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT 
    pe.plugin_id, 
    pe.key, 
    pe.route, 
    pe.label, 
    pe.menu_group,
    GROUP_CONCAT(pp.permission) as required_permissions,
    GROUP_CONCAT(pp.granted) as granted_flags
FROM plugin_entries pe
JOIN plugins p ON p.id = pe.plugin_id
LEFT JOIN plugin_permissions pp ON pp.plugin_id = pe.plugin_id
WHERE pe.type = 'page' AND pe.is_active = 1 AND p.is_active = 1
GROUP BY pe.plugin_id, pe.key, pe.route, pe.label, pe.menu_group
ORDER BY pe.sort_order, pe.label
`)
	if err != nil {
		return nil, pluginObs.wrap("list_menu_entries", err)
	}
	defer rows.Close()
	var res []MenuEntryRow
	for rows.Next() {
		var row MenuEntryRow
		if err := rows.Scan(&row.PluginID, &row.Key, &row.Route, &row.Label, &row.MenuGroup, &row.RequiredPermissions, &row.GrantedFlags); err != nil {
			return nil, pluginObs.wrap("list_menu_entries", err)
		}
		res = append(res, row)
	}
	return res, rows.Err()
}

// ListReceiptTemplates returns active receipt template entries with plugin metadata.
func (r *PluginRepo) ListReceiptTemplates(ctx context.Context) ([]ReceiptTemplateRow, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT 
    p.id,
    p.name,
    p.version,
    pe.key,
    pe.sort_order,
    COALESCE(pe.config_json, '')
FROM plugin_entries pe
JOIN plugins p ON p.id = pe.plugin_id
WHERE pe.type = 'receipt_template' AND pe.is_active = 1 AND p.is_active = 1
ORDER BY pe.sort_order, pe.plugin_id, pe.key
`)
	if err != nil {
		return nil, pluginObs.wrap("list_receipt_templates", err)
	}
	defer rows.Close()
	var res []ReceiptTemplateRow
	for rows.Next() {
		var row ReceiptTemplateRow
		if err := rows.Scan(&row.PluginID, &row.PluginName, &row.PluginVersion, &row.EntryKey, &row.SortOrder, &row.ConfigJSON); err != nil {
			return nil, pluginObs.wrap("list_receipt_templates", err)
		}
		res = append(res, row)
	}
	return res, rows.Err()
}

// ListPluginHookEvents returns the events a plugin's active hooks subscribe
// to (manifest "hooks" — what its wasm handler consumes).
func (r *PluginRepo) ListPluginHookEvents(ctx context.Context, pluginID string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT DISTINCT event FROM plugin_hooks
WHERE plugin_id = ? AND is_active = 1`, pluginID)
	if err != nil {
		return nil, pluginObs.wrap("list_hook_events", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var ev string
		if err := rows.Scan(&ev); err != nil {
			return nil, pluginObs.wrap("list_hook_events", err)
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

// PaymentEntryRow is a tender method contributed by an active plugin.
type PaymentEntryRow struct {
	PluginID     string
	PluginName   string
	EntryKey     string
	Label        string
	TriggerEvent string
	ConfigJSON   string
}

// ListPaymentEntries returns payment entries from active plugins.
func (r *PluginRepo) ListPaymentEntries(ctx context.Context) ([]PaymentEntryRow, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT p.id, p.name, pe.key, pe.label, COALESCE(pe.trigger_event, ''), COALESCE(pe.config_json, '')
FROM plugin_entries pe
JOIN plugins p ON p.id = pe.plugin_id
WHERE pe.type = 'payment' AND pe.is_active = 1 AND p.is_active = 1
ORDER BY pe.sort_order, pe.label`)
	if err != nil {
		return nil, fmt.Errorf("list payment entries: %w", err)
	}
	defer rows.Close()
	var out []PaymentEntryRow
	for rows.Next() {
		var e PaymentEntryRow
		if err := rows.Scan(&e.PluginID, &e.PluginName, &e.EntryKey, &e.Label, &e.TriggerEvent, &e.ConfigJSON); err != nil {
			return nil, fmt.Errorf("scan payment entry: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// PaymentKeyConflict is an install-time payment-entry key collision.
type PaymentKeyConflict struct {
	Key            string
	Owner          string // owning plugin id; empty = built-in or shop-created tender
	OwnerInstalled bool   // false when the owning plugin's row is gone (uninstalled; its tender row is retained for sales history)
}

// FindPaymentKeyConflicts returns, for the candidate payment-entry keys of
// pluginID, those already taken by a non-plugin payment method (built-in or
// shop-created via EnsurePaymentMethod) or by ANOTHER plugin's payment
// entry. The plugin's own existing keys never conflict, so reinstall and
// upgrade stay clean. Companion to SyncPluginPaymentMethods' ownership
// guard (ADR-0031): the guard makes a collision harmless, this makes it
// loud at install time.
func (r *PluginRepo) FindPaymentKeyConflicts(ctx context.Context, tx *sql.Tx, pluginID string, keys []string) ([]PaymentKeyConflict, error) {
	exec := r.executor(tx)
	var out []PaymentKeyConflict
	for _, key := range keys {
		var owner sql.NullString
		var ownerInstalled int
		err := exec.QueryRowContext(ctx, `
SELECT pm.plugin_id, CASE WHEN p.id IS NULL THEN 0 ELSE 1 END
FROM payment_methods pm
LEFT JOIN plugins p ON p.id = pm.plugin_id
WHERE pm.id = ?`, key).Scan(&owner, &ownerInstalled)
		switch {
		case err == sql.ErrNoRows:
			// no method row; fall through to the entry check
		case err != nil:
			return nil, pluginObs.wrap("payment_key_conflicts", err)
		case !owner.Valid || owner.String == "":
			out = append(out, PaymentKeyConflict{Key: key})
			continue
		case owner.String != pluginID:
			out = append(out, PaymentKeyConflict{Key: key, Owner: owner.String, OwnerInstalled: ownerInstalled == 1})
			continue
		}
		var entryOwner string
		err = exec.QueryRowContext(ctx, `
SELECT plugin_id FROM plugin_entries
WHERE type = 'payment' AND key = ? AND plugin_id != ? LIMIT 1`, key, pluginID).Scan(&entryOwner)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return nil, pluginObs.wrap("payment_key_conflicts", err)
		}
		out = append(out, PaymentKeyConflict{Key: key, Owner: entryOwner, OwnerInstalled: true})
	}
	return out, nil
}

// FindPaymentNameConflicts returns, for the candidate payment-entry labels
// of pluginID, those already taken by a non-plugin payment method (built-in
// or shop-created) or by ANOTHER plugin's payment entry — checking both
// payment_methods.name (also UNIQUE) AND plugin_entries.label directly,
// same two-step shape as FindPaymentKeyConflicts, since a just-installed
// sibling plugin's entries haven't necessarily been synced into
// payment_methods yet. A plugin choosing a label that collides with an
// existing tender's name would otherwise hard-fail the sync's INSERT at
// every startup (ut-docs#16, ADR-0031's documented residual). The plugin's
// own existing rows never conflict, so reinstall/upgrade stay clean.
func (r *PluginRepo) FindPaymentNameConflicts(ctx context.Context, tx *sql.Tx, pluginID string, names []string) ([]PaymentKeyConflict, error) {
	exec := r.executor(tx)
	var out []PaymentKeyConflict
	for _, name := range names {
		var owner sql.NullString
		var ownerInstalled int
		err := exec.QueryRowContext(ctx, `
SELECT pm.plugin_id, CASE WHEN p.id IS NULL THEN 0 ELSE 1 END
FROM payment_methods pm
LEFT JOIN plugins p ON p.id = pm.plugin_id
WHERE pm.name = ? AND COALESCE(pm.plugin_id, '') != ?`, name, pluginID).Scan(&owner, &ownerInstalled)
		switch {
		case err == sql.ErrNoRows:
			// no method row; fall through to the entry check
		case err != nil:
			return nil, pluginObs.wrap("payment_name_conflicts", err)
		case !owner.Valid || owner.String == "":
			out = append(out, PaymentKeyConflict{Key: name})
			continue
		default:
			out = append(out, PaymentKeyConflict{Key: name, Owner: owner.String, OwnerInstalled: ownerInstalled == 1})
			continue
		}
		var entryOwner string
		err = exec.QueryRowContext(ctx, `
SELECT plugin_id FROM plugin_entries
WHERE type = 'payment' AND label = ? AND plugin_id != ? LIMIT 1`, name, pluginID).Scan(&entryOwner)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return nil, pluginObs.wrap("payment_name_conflicts", err)
		}
		out = append(out, PaymentKeyConflict{Key: name, Owner: entryOwner, OwnerInstalled: true})
	}
	return out, nil
}

// PageKeyConflict is an install-time type:"page" entry key collision.
type PageKeyConflict struct {
	Key   string
	Owner string // owning plugin id
}

// FindPageKeyConflicts returns, for the candidate type:"page" entry keys of
// pluginID, those already owned by another plugin's type:"page" entry.
// internal/plugins.Manager.MenuPlugins (and the /ext/{key} dispatch it
// backs, internal/pages/external_api.go) is keyed by bare entry key, so an
// unchecked collision silently overwrites one plugin's menu tile/route with
// another's (ut-docs#472). Unlike payment keys, a page entry has no
// standalone table it can outlive — plugin_entries FK CASCADEs when a
// plugin row is deleted — so any row found here belongs to a plugin that
// still exists. The plugin's own existing keys never conflict, so
// reinstall/upgrade stay clean. Companion to validatePaymentEntryKeys'
// pattern, deliberately reused for a second entry type rather than
// namespacing MenuPlugins itself, to keep the two conventions consistent.
func (r *PluginRepo) FindPageKeyConflicts(ctx context.Context, tx *sql.Tx, pluginID string, keys []string) ([]PageKeyConflict, error) {
	exec := r.executor(tx)
	var out []PageKeyConflict
	for _, key := range keys {
		var owner string
		err := exec.QueryRowContext(ctx, `
SELECT plugin_id FROM plugin_entries
WHERE type = 'page' AND key = ? AND plugin_id != ? LIMIT 1`, key, pluginID).Scan(&owner)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return nil, pluginObs.wrap("page_key_conflicts", err)
		}
		out = append(out, PageKeyConflict{Key: key, Owner: owner})
	}
	return out, nil
}

// PageRouteConflict is an install-time type:"page" entry route collision —
// the sibling of PageKeyConflict for a second, independent namespace.
type PageRouteConflict struct {
	Route string
	Owner string // owning plugin id
}

// FindPageRouteConflicts returns, for the candidate type:"page" entry routes
// of pluginID, those already owned by another plugin's type:"page" entry.
// internal/pages/plugin_page.go's findPageEntry resolves GET /plugin/… by
// matching route against ListPageEntries' rows and returning the first hit
// (ORDER BY sort_order, plugin_id, key) — an unchecked collision means
// whichever plugin sorts first silently serves every request to that route,
// with the second plugin's page unreachable and no signal to either author
// or the shop owner (ut-docs#499, found reviewing ut-docs#472's sibling
// `key` check). Unlike DocsEntryKey's key exemption, no route is exempt
// here: ADR-0037 has every plugin — docs entries included — declare its own
// route ("/plugin/<its-usual-route>"), so a route collision, docs or not,
// is always a genuine authoring conflict, never the expected shape. An
// empty route entry is not dispatchable via findPageEntry at all (it
// requires e.Route != ""), so it cannot collide and is skipped by the
// caller before this is ever called with it.
func (r *PluginRepo) FindPageRouteConflicts(ctx context.Context, tx *sql.Tx, pluginID string, routes []string) ([]PageRouteConflict, error) {
	exec := r.executor(tx)
	var out []PageRouteConflict
	for _, route := range routes {
		var owner string
		err := exec.QueryRowContext(ctx, `
SELECT plugin_id FROM plugin_entries
WHERE type = 'page' AND route = ? AND plugin_id != ? LIMIT 1`, route, pluginID).Scan(&owner)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return nil, pluginObs.wrap("page_route_conflicts", err)
		}
		out = append(out, PageRouteConflict{Route: route, Owner: owner})
	}
	return out, nil
}

// OrphanedPaymentMethod is a plugin-owned payment_methods row whose owning
// plugin no longer exists on this till — a shop-created or built-in tender
// captured before ADR-0031's ownership guard shipped, which migration 021
// only repairs for the three seeded built-ins (cash/card/gift). A row alone
// can't distinguish that capture from a genuine, still-valid plugin method
// whose plugin was simply uninstalled (sales history retains those rows
// deliberately) — this is surfaced as a startup warning, not auto-repaired.
type OrphanedPaymentMethod struct {
	ID       string
	Name     string
	PluginID string
}

// FindOrphanedPaymentMethods returns every payment_methods row stamped with
// a plugin_id that has no corresponding plugins row.
func (r *PluginRepo) FindOrphanedPaymentMethods(ctx context.Context) ([]OrphanedPaymentMethod, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT pm.id, pm.name, pm.plugin_id
FROM payment_methods pm
WHERE pm.plugin_id IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM plugins p WHERE p.id = pm.plugin_id)`)
	if err != nil {
		return nil, pluginObs.wrap("find_orphaned_payment_methods", err)
	}
	defer rows.Close()
	var out []OrphanedPaymentMethod
	for rows.Next() {
		var o OrphanedPaymentMethod
		if err := rows.Scan(&o.ID, &o.Name, &o.PluginID); err != nil {
			return nil, pluginObs.wrap("find_orphaned_payment_methods", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// SuppressedPaymentNameEntry is an active plugin payment entry that could
// not materialize (or rename) into its own payment_methods row because
// another row already holds its label — the sync's name-collision guards
// (SyncPluginPaymentMethods) leave the entry's row alone rather than error,
// so this is how the caller (plugins.Init) turns that silence into a
// startup warning instead of a tender quietly not appearing (ut-docs#16,
// found by the independent review of the first pass at this fix).
type SuppressedPaymentNameEntry struct {
	PluginID   string
	Key        string
	Label      string
	BlockingID string // the payment_methods row already holding Label
}

// FindSuppressedPaymentNameEntries returns every active plugin payment
// entry whose (key, label) hasn't fully landed in payment_methods because a
// DIFFERENT row already owns that label.
func (r *PluginRepo) FindSuppressedPaymentNameEntries(ctx context.Context) ([]SuppressedPaymentNameEntry, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT pe.plugin_id, pe.key, pe.label, blocker.id
FROM plugin_entries pe
JOIN plugins p ON p.id = pe.plugin_id
JOIN payment_methods blocker ON blocker.name = pe.label AND blocker.id != pe.key
WHERE pe.type = 'payment' AND pe.is_active = 1 AND p.is_active = 1
  AND NOT EXISTS (
      SELECT 1 FROM payment_methods pm
      WHERE pm.id = pe.key AND pm.name = pe.label AND pm.plugin_id = pe.plugin_id
  )`)
	if err != nil {
		return nil, pluginObs.wrap("find_suppressed_payment_name_entries", err)
	}
	defer rows.Close()
	var out []SuppressedPaymentNameEntry
	for rows.Next() {
		var s SuppressedPaymentNameEntry
		if err := rows.Scan(&s.PluginID, &s.Key, &s.Label, &s.BlockingID); err != nil {
			return nil, pluginObs.wrap("find_suppressed_payment_name_entries", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// SyncPluginPaymentMethods derives payment_methods rows from active plugins'
// payment entries: upserts a method per entry (id = entry key) and
// deactivates plugin-backed methods whose plugin is gone or disabled.
// Rows are never deleted — payments history references them.
//
// Ownership guard (ADR-0031): the upsert only updates a conflicting row the
// same plugin already owns. A key colliding with a built-in (plugin_id NULL)
// or another plugin's method leaves the existing row untouched — the entry
// simply doesn't materialize (and install-time validation in
// plugins.PersistManifest rejects such keys with a clear error). Before this
// guard a plugin declaring key "cash" captured the built-in cash tender, and
// the deactivate step below then removed cash from checkout entirely when
// the plugin went away (migration 021 repairs tills hit by that).
//
// Two defenses against payment_methods.name's separate UNIQUE constraint
// (SQLite routes an upsert to whichever clause's target matches the
// constraint actually violated), both needed — an independent review of
// the first found the DO UPDATE branch alone still reachable and boot-
// fatal (ut-docs#16):
//   - The INSERT path carries a second ON CONFLICT(name) DO NOTHING: a
//     LEGACY entry whose label collides with an existing tender's name (an
//     install predating FindPaymentNameConflicts) simply doesn't
//     materialize instead of aborting this whole statement.
//   - The DO UPDATE's own `name` write is guarded by a CASE: a plugin
//     renaming (or swapping labels between) its OWN already-materialized
//     entries — accepted at install time since neither label conflicts
//     with the OTHER plugin's rows — must not itself attempt writing a
//     name another row still holds. Without this, every future startup's
//     sync hard-aborted on the very next run after such a rename.
//
// Both leave the stale name in place rather than erroring — the entry
// simply doesn't relabel until the collision clears, same "silently
// doesn't materialize" treatment id collisions already get.
func (r *PluginRepo) SyncPluginPaymentMethods(ctx context.Context) error {
	// Standing invariant, re-asserted EVERY run (not just migration 021):
	// the seeded built-ins are never plugin-owned. Damage can re-enter a
	// repaired till after the one-shot migration — e.g. LAN admin sync
	// from a not-yet-upgraded primary — and the hijacking plugin may not
	// even exist locally, so the deactivate step below would otherwise pin
	// the row inactive forever.
	if _, err := r.db.ExecContext(ctx, `
UPDATE payment_methods SET plugin_id = NULL, is_active = 1
WHERE id IN ('cash', 'card', 'gift') AND plugin_id IS NOT NULL`); err != nil {
		return fmt.Errorf("sync plugin payment methods (built-in invariant): %w", err)
	}
	if _, err := r.db.ExecContext(ctx, `
INSERT INTO payment_methods (id, name, type, is_active, sort_order, plugin_id)
SELECT pe.key, pe.label,
       COALESCE(CASE WHEN json_valid(pe.config_json) THEN json_extract(pe.config_json, '$.method_type') END, 'card'),
       1, 100 + pe.sort_order, pe.plugin_id
FROM plugin_entries pe
JOIN plugins p ON p.id = pe.plugin_id
WHERE pe.type = 'payment' AND pe.is_active = 1 AND p.is_active = 1
ON CONFLICT(id) DO UPDATE SET
    name = CASE WHEN EXISTS (
        SELECT 1 FROM payment_methods pm2
        WHERE pm2.name = excluded.name AND pm2.id != payment_methods.id
    ) THEN payment_methods.name ELSE excluded.name END,
    is_active = 1
WHERE payment_methods.plugin_id = excluded.plugin_id
ON CONFLICT(name) DO NOTHING`); err != nil {
		return fmt.Errorf("sync plugin payment methods (upsert): %w", err)
	}
	// Ownership-aware: only the OWNING plugin's live entries keep a row
	// active — an unrelated plugin's entry with the same key must not.
	if _, err := r.db.ExecContext(ctx, `
UPDATE payment_methods SET is_active = 0
WHERE plugin_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM plugin_entries pe
    JOIN plugins p ON p.id = pe.plugin_id
    WHERE pe.type = 'payment' AND pe.is_active = 1 AND p.is_active = 1
      AND pe.key = payment_methods.id AND pe.plugin_id = payment_methods.plugin_id
)`); err != nil {
		return fmt.Errorf("sync plugin payment methods (deactivate): %w", err)
	}
	return nil
}

// PageEntryRow is a plugin-provided page (plugin_entries type='page') with
// the installed plugin version so callers can locate its on-disk content.
type PageEntryRow struct {
	PluginID      string
	PluginName    string
	PluginVersion string
	EntryKey      string
	Label         string
	Route         string
	ConfigJSON    string
}

// ListPageEntries returns active page entries from active plugins.
func (r *PluginRepo) ListPageEntries(ctx context.Context) ([]PageEntryRow, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT
    p.id,
    p.name,
    p.version,
    pe.key,
    pe.label,
    COALESCE(pe.route, ''),
    COALESCE(pe.config_json, '')
FROM plugin_entries pe
JOIN plugins p ON p.id = pe.plugin_id
WHERE pe.type = 'page' AND pe.is_active = 1 AND p.is_active = 1
ORDER BY pe.sort_order, pe.plugin_id, pe.key
`)
	if err != nil {
		return nil, pluginObs.wrap("list_page_entries", err)
	}
	defer rows.Close()
	var res []PageEntryRow
	for rows.Next() {
		var row PageEntryRow
		if err := rows.Scan(&row.PluginID, &row.PluginName, &row.PluginVersion, &row.EntryKey, &row.Label, &row.Route, &row.ConfigJSON); err != nil {
			return nil, pluginObs.wrap("list_page_entries", err)
		}
		res = append(res, row)
	}
	return res, rows.Err()
}

// ButtonEntryRow is a plugin-provided action button (plugin_entries
// type='button'). TargetAction / TriggerEvent name the event the POS publishes
// when the button is pressed.
type ButtonEntryRow struct {
	PluginID      string
	PluginVersion string
	PluginName    string
	EntryKey      string
	Label         string
	IconPath      string
	ParentPageKey string
	TargetAction  string
	TriggerEvent  string
	SortOrder     int
	ConfigJSON    string
}

// ListButtonEntries returns active button entries from active plugins.
func (r *PluginRepo) ListButtonEntries(ctx context.Context) ([]ButtonEntryRow, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT
    p.id,
    p.version,
    p.name,
    pe.key,
    pe.label,
    COALESCE(pe.icon_path, ''),
    COALESCE(pe.parent_page_key, ''),
    COALESCE(pe.target_action, ''),
    COALESCE(pe.trigger_event, ''),
    pe.sort_order,
    COALESCE(pe.config_json, '')
FROM plugin_entries pe
JOIN plugins p ON p.id = pe.plugin_id
WHERE pe.type = 'button' AND pe.is_active = 1 AND p.is_active = 1
ORDER BY pe.sort_order, pe.plugin_id, pe.key
`)
	if err != nil {
		return nil, pluginObs.wrap("list_button_entries", err)
	}
	defer rows.Close()
	var res []ButtonEntryRow
	for rows.Next() {
		var row ButtonEntryRow
		if err := rows.Scan(&row.PluginID, &row.PluginVersion, &row.PluginName, &row.EntryKey, &row.Label, &row.IconPath, &row.ParentPageKey, &row.TargetAction, &row.TriggerEvent, &row.SortOrder, &row.ConfigJSON); err != nil {
			return nil, pluginObs.wrap("list_button_entries", err)
		}
		res = append(res, row)
	}
	return res, rows.Err()
}

// ExportEntryRow is a plugin-provided export/report trigger (plugin_entries
// type='export' or type='report'). The manager-gated /api/data/export
// dispatcher (internal/pages/data_api.go) resolves Key to this row, then
// asks PluginID specifically (EventBus.AskPlugin("export.requested.ask",
// ...) — never a broadcast Ask, which would let a different installed
// plugin answer on this entry's behalf) to produce the export. Entities is
// the entry's own manifest declaration (ManifestEntry.Entities), persisted
// inside config_json exactly like ImportEntryRow.Entities (ut-docs#600,
// mirroring #599's import-side pattern) — it's what lets the dispatcher
// gate a catalog-entity ledger (e.g. "items") on both a declared entity AND
// a permission grant, rather than every export entry silently being asked
// for every ledger it happens to hold read access to.
type ExportEntryRow struct {
	PluginID  string
	Key       string
	Label     string
	SortOrder int
	Entities  []string
}

// ListExportEntries returns active export/report entries from active
// plugins — the Data-management settings page's export dispatcher populates
// its entry picker from this.
func (r *PluginRepo) ListExportEntries(ctx context.Context) ([]ExportEntryRow, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT
    p.id,
    pe.key,
    pe.label,
    pe.sort_order,
    COALESCE(pe.config_json, '')
FROM plugin_entries pe
JOIN plugins p ON p.id = pe.plugin_id
WHERE pe.type IN ('export', 'report') AND pe.is_active = 1 AND p.is_active = 1
ORDER BY pe.sort_order, pe.plugin_id, pe.key
`)
	if err != nil {
		return nil, pluginObs.wrap("list_export_entries", err)
	}
	defer rows.Close()
	var res []ExportEntryRow
	for rows.Next() {
		var row ExportEntryRow
		var configJSON string
		if err := rows.Scan(&row.PluginID, &row.Key, &row.Label, &row.SortOrder, &configJSON); err != nil {
			return nil, pluginObs.wrap("list_export_entries", err)
		}
		if configJSON != "" {
			// Same unpack shape as ListImportEntries: a malformed
			// config_json (hand-edited DB, legacy row) just yields an empty
			// Entities — the dispatcher then finds no declared entity and
			// omits the ledger rather than this listing failing wholesale.
			var cfg struct {
				Entities []string `json:"entities"`
			}
			if err := json.Unmarshal([]byte(configJSON), &cfg); err == nil {
				row.Entities = cfg.Entities
			}
		}
		res = append(res, row)
	}
	return res, rows.Err()
}

// ImportEntryRow is a plugin-provided import handler (plugin_entries
// type='import'). The manager-gated /api/data/import dispatcher
// (internal/pages/import_dispatch.go, ut-docs#599) resolves Key to this
// row, then asks PluginID specifically (EventBus.AskPlugin(
// "import.requested.ask", ...) — never a broadcast Ask, same rule as
// ExportEntryRow above) to consume the staged upload. Entities/FileFormats
// are the entry's own manifest declarations (ManifestEntry.Entities /
// .FileFormats), persisted inside config_json by
// plugins.PersistManifest/Rollback and unpacked here so callers get typed
// slices rather than raw JSON.
type ImportEntryRow struct {
	PluginID    string
	Key         string
	Label       string
	SortOrder   int
	Entities    []string
	FileFormats []string
}

// ListImportEntries returns active import entries from active plugins —
// the /api/data/import dispatcher resolves its entry_key against this.
func (r *PluginRepo) ListImportEntries(ctx context.Context) ([]ImportEntryRow, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT
    p.id,
    pe.key,
    pe.label,
    pe.sort_order,
    COALESCE(pe.config_json, '')
FROM plugin_entries pe
JOIN plugins p ON p.id = pe.plugin_id
WHERE pe.type = 'import' AND pe.is_active = 1 AND p.is_active = 1
ORDER BY pe.sort_order, pe.plugin_id, pe.key
`)
	if err != nil {
		return nil, pluginObs.wrap("list_import_entries", err)
	}
	defer rows.Close()
	var res []ImportEntryRow
	for rows.Next() {
		var row ImportEntryRow
		var configJSON string
		if err := rows.Scan(&row.PluginID, &row.Key, &row.Label, &row.SortOrder, &configJSON); err != nil {
			return nil, pluginObs.wrap("list_import_entries", err)
		}
		if configJSON != "" {
			// Unpack the persisted declarations; a malformed config_json
			// (hand-edited DB, legacy row) just yields empty slices — the
			// dispatcher then finds no declared entities and 400s cleanly
			// rather than this listing failing wholesale.
			var cfg struct {
				Entities    []string `json:"entities"`
				FileFormats []string `json:"file_formats"`
			}
			if err := json.Unmarshal([]byte(configJSON), &cfg); err == nil {
				row.Entities = cfg.Entities
				row.FileFormats = cfg.FileFormats
			}
		}
		res = append(res, row)
	}
	return res, rows.Err()
}

// ThemeRow is a UI theme provided by an installed plugin (plugin_entries
// type='theme'). ConfigJSON carries the theme config, e.g. {"css":"assets/theme.css"}.
type ThemeRow struct {
	PluginID      string
	PluginVersion string
	EntryKey      string
	Label         string
	ConfigJSON    string
}

// ListThemeEntries returns active theme entries from active plugins.
func (r *PluginRepo) ListThemeEntries(ctx context.Context) ([]ThemeRow, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT
    p.id,
    p.version,
    pe.key,
    pe.label,
    COALESCE(pe.config_json, '')
FROM plugin_entries pe
JOIN plugins p ON p.id = pe.plugin_id
WHERE pe.type = 'theme' AND pe.is_active = 1 AND p.is_active = 1
ORDER BY pe.sort_order, pe.plugin_id, pe.key
`)
	if err != nil {
		return nil, pluginObs.wrap("list_theme_entries", err)
	}
	defer rows.Close()
	var res []ThemeRow
	for rows.Next() {
		var row ThemeRow
		if err := rows.Scan(&row.PluginID, &row.PluginVersion, &row.EntryKey, &row.Label, &row.ConfigJSON); err != nil {
			return nil, pluginObs.wrap("list_theme_entries", err)
		}
		res = append(res, row)
	}
	return res, rows.Err()
}

// HasActivePrinterPermission reports whether any active plugin can access printers.
func (r *PluginRepo) HasActivePrinterPermission(ctx context.Context) (bool, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM plugin_permissions pp
JOIN plugins p ON p.id = pp.plugin_id
WHERE pp.permission = 'devices:printer' AND pp.granted = 1 AND p.is_active = 1
`).Scan(&count)
	if err != nil {
		return false, pluginObs.wrap("has_active_printer", err)
	}
	return count > 0, nil
}

// HasActivePrinterCapability checks if an active plugin is permitted to use printers.
func (r *PluginRepo) HasActivePrinterCapability(ctx context.Context) (bool, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM plugin_permissions pp
JOIN plugins p ON p.id = pp.plugin_id
WHERE pp.permission = 'devices:printer' AND pp.granted = 1 AND p.is_active = 1 AND p.install_state = 'installed'
`).Scan(&count)
	if err != nil {
		return false, pluginObs.wrap("has_printer_capability", err)
	}
	return count > 0, nil
}

// GetPluginVersionAt returns the plugin version active at or before the given timestamp.
func (r *PluginRepo) GetPluginVersionAt(ctx context.Context, pluginID string, at time.Time) (string, bool, error) {
	query := `
SELECT version
FROM plugins
WHERE id = ? AND updated_at <= ?
ORDER BY updated_at DESC
LIMIT 1
`
	var version string
	err := r.db.QueryRowContext(ctx, query, pluginID, at.UTC().Format(time.RFC3339)).Scan(&version)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, pluginObs.wrap("get_plugin_version_at", err)
	}
	return version, true, nil
}

// ListCatalog returns non-deprecated catalog entries.
func (r *PluginRepo) ListCatalog(ctx context.Context) ([]CatalogRow, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, version, name, description, runtime, entrypoint, package_url, sha256, author, website, COALESCE(tags_json,'')
FROM plugin_catalog
WHERE is_deprecated = 0
`)
	if err != nil {
		return nil, pluginObs.wrap("list_catalog", err)
	}
	defer rows.Close()
	var res []CatalogRow
	for rows.Next() {
		var row CatalogRow
		if err := rows.Scan(&row.ID, &row.Version, &row.Name, &row.Description, &row.Runtime, &row.Entrypoint, &row.PackageURL, &row.SHA256, &row.Author, &row.Website, &row.TagsJSON); err != nil {
			return nil, pluginObs.wrap("list_catalog", err)
		}
		res = append(res, row)
	}
	return res, rows.Err()
}

// CatalogPage returns paginated catalog entries and total count filtered by tag.
func (r *PluginRepo) CatalogPage(ctx context.Context, offset, limit int, tag string) ([]CatalogRow, int, error) {
	where := "WHERE is_deprecated = 0"
	args := []any{}
	if strings.TrimSpace(tag) != "" {
		where += " AND tags_json LIKE ?"
		args = append(args, "%"+tag+"%")
	}

	var total int
	countQuery := fmt.Sprintf(`SELECT COUNT(*) FROM plugin_catalog %s`, where)
	if err := r.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, pluginObs.wrap("catalog_count", err)
	}

	args = append(args, limit, offset)
	query := fmt.Sprintf(`
SELECT id, version, name, description, runtime, entrypoint, package_url, sha256, author, website, COALESCE(tags_json,'')
FROM plugin_catalog
%s
ORDER BY name
LIMIT ? OFFSET ?
`, where)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, pluginObs.wrap("catalog_page", err)
	}
	defer rows.Close()

	var res []CatalogRow
	for rows.Next() {
		var row CatalogRow
		if err := rows.Scan(&row.ID, &row.Version, &row.Name, &row.Description, &row.Runtime, &row.Entrypoint, &row.PackageURL, &row.SHA256, &row.Author, &row.Website, &row.TagsJSON); err != nil {
			return nil, 0, pluginObs.wrap("catalog_page", err)
		}
		res = append(res, row)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, pluginObs.wrap("catalog_page", err)
	}
	return res, total, nil
}

type executor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	PrepareContext(ctx context.Context, query string) (*sql.Stmt, error)
}

func (r *PluginRepo) executor(tx *sql.Tx) executor {
	if tx != nil {
		return tx
	}
	return r.db
}
