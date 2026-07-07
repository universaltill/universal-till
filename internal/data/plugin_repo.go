package data

import (
	"context"
	"database/sql"
	"encoding/json"
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
func (r *PluginRepo) InsertPluginSettings(ctx context.Context, tx *sql.Tx, settings []PluginSettingRow) error {
	if len(settings) == 0 {
		return nil
	}
	exec := r.executor(tx)
	stmt, err := exec.PrepareContext(ctx, `
INSERT INTO plugin_settings (
	id, plugin_id, key, value_json, scope, scope_id, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(plugin_id, key, scope, scope_id) DO UPDATE SET
	value_json = excluded.value_json,
	updated_at = excluded.updated_at
`)
	if err != nil {
		return pluginObs.wrap("prepare_settings", err)
	}
	defer stmt.Close()
	for _, s := range settings {
		if s.ID == "" {
			s.ID = uuid.NewString()
		}
		if _, err := stmt.ExecContext(ctx, s.ID, s.PluginID, s.Key, s.ValueJSON, s.Scope, s.ScopeID, s.UpdatedAt.UTC().Format(time.RFC3339)); err != nil {
			return pluginObs.wrap("insert_setting", err)
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
	ID      string
	Name    string
	Version string
	Author  string
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

func (r *PluginRepo) ListInstalledPlugins(ctx context.Context) ([]InstalledPluginRow, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, name, version, '' as author
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
		if err := rows.Scan(&p.ID, &p.Name, &p.Version, &p.Author); err != nil {
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
