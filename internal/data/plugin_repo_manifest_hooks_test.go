package data

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// seedCatalogAndPlugin gives a plugin id/version both the catalog row required
// by the plugins(id,version) FK and the plugin row itself, mirroring the real
// PersistManifest sequence (EnsureCatalogEntry then UpsertPluginManifest).
func seedCatalogAndPlugin(t *testing.T, ctx context.Context, repo *PluginRepo, id, version string) {
	t.Helper()
	if err := repo.EnsureCatalogEntry(ctx, nil, CatalogUpsertRow{
		ID: id, Version: version, Name: "Test Plugin", Runtime: "wasm",
		Entrypoint: "p.wasm", PackageURL: "u", SHA256: "s", MinPOSVersion: "0.1.0",
		PublishedAt: "2026-07-30",
	}); err != nil {
		t.Fatalf("seed catalog: %v", err)
	}
	now := time.Now().UTC()
	if err := repo.UpsertPluginManifest(ctx, nil, ManifestRow{
		ID: id, Name: "Test Plugin", Version: version, InstallState: "installed",
		Entrypoint: "p.wasm", Runtime: "wasm", TrustLevel: "untrusted",
		InstalledAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed plugin: %v", err)
	}
}

func TestPluginRepo_UpsertPluginManifest_InsertsAndUpdates(t *testing.T) {
	ctx := context.Background()
	d := openMigratedDB(t, "till.db")
	repo := NewPluginRepo(d.DB)
	seedCatalogAndPlugin(t, ctx, repo, "com.t.p", "1.0.0")

	row, found, err := repo.GetPlugin(ctx, "com.t.p", "")
	if err != nil || !found {
		t.Fatalf("GetPlugin after insert: found=%v err=%v", found, err)
	}
	if row.Version != "1.0.0" || !row.IsActive {
		t.Fatalf("unexpected row after insert: %+v", row)
	}

	// Re-apply with a new version/entrypoint: must update in place (ON CONFLICT).
	// The FK requires a matching catalog row for the new version first.
	if err := repo.EnsureCatalogEntry(ctx, nil, CatalogUpsertRow{
		ID: "com.t.p", Version: "2.0.0", Name: "Test Plugin", Runtime: "wasm",
		Entrypoint: "p2.wasm", PackageURL: "u", SHA256: "s2", MinPOSVersion: "0.1.0",
		PublishedAt: "2026-07-30",
	}); err != nil {
		t.Fatalf("seed catalog v2: %v", err)
	}
	now := time.Now().UTC()
	if err := repo.UpsertPluginManifest(ctx, nil, ManifestRow{
		ID: "com.t.p", Name: "Test Plugin", Version: "2.0.0", InstallState: "installed",
		Entrypoint: "p2.wasm", Runtime: "wasm", TrustLevel: "trusted",
		InstalledAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("upsert v2: %v", err)
	}

	row, found, err = repo.GetPlugin(ctx, "com.t.p", "")
	if err != nil || !found {
		t.Fatalf("GetPlugin after update: found=%v err=%v", found, err)
	}
	if row.Version != "2.0.0" || row.Entrypoint != "p2.wasm" {
		t.Fatalf("expected updated version/entrypoint, got %+v", row)
	}
}

func TestPluginRepo_UpsertPluginManifest_WithTx(t *testing.T) {
	ctx := context.Background()
	d := openMigratedDB(t, "till.db")
	repo := NewPluginRepo(d.DB)
	if err := repo.EnsureCatalogEntry(ctx, nil, CatalogUpsertRow{
		ID: "com.t.tx", Version: "1.0.0", Name: "TxPlugin", Runtime: "wasm",
		Entrypoint: "p.wasm", PackageURL: "u", SHA256: "s", MinPOSVersion: "0.1.0",
		PublishedAt: "2026-07-30",
	}); err != nil {
		t.Fatalf("seed catalog: %v", err)
	}
	tx, err := d.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	now := time.Now().UTC()
	if err := repo.UpsertPluginManifest(ctx, tx, ManifestRow{
		ID: "com.t.tx", Name: "TxPlugin", Version: "1.0.0", InstallState: "installed",
		Entrypoint: "p.wasm", Runtime: "wasm", TrustLevel: "untrusted",
		InstalledAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("upsert in tx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, found, err := repo.GetPlugin(ctx, "com.t.tx", ""); err != nil || !found {
		t.Fatalf("plugin not visible after commit: found=%v err=%v", found, err)
	}
}

func TestPluginRepo_DeletePlugin_CascadesButNotStorage(t *testing.T) {
	ctx := context.Background()
	d := openMigratedDB(t, "till.db")
	repo := NewPluginRepo(d.DB)
	seedCatalogAndPlugin(t, ctx, repo, "com.t.del", "1.0.0")

	mustExec(t, d, `INSERT INTO plugin_entries (id,plugin_id,type,key,label) VALUES ('e1','com.t.del','page','home','Home')`)
	mustExec(t, d, `INSERT INTO plugin_settings (id,plugin_id,key,value_json) VALUES ('s1','com.t.del','k','"v"')`)
	if err := repo.InsertPluginHooks(ctx, nil, []PluginHookRow{{PluginID: "com.t.del", Event: "sale.completed", Action: "a", IsActive: true}}); err != nil {
		t.Fatalf("seed hook: %v", err)
	}
	if err := repo.InsertPluginPermissions(ctx, nil, "com.t.del", []string{"storage"}); err != nil {
		t.Fatalf("seed permission: %v", err)
	}
	if err := repo.StorageSet(ctx, "com.t.del", "k", []byte("v")); err != nil {
		t.Fatalf("seed storage: %v", err)
	}

	if err := repo.DeletePlugin(ctx, nil, "com.t.del"); err != nil {
		t.Fatalf("DeletePlugin: %v", err)
	}

	for _, tbl := range []string{"plugin_entries", "plugin_settings", "plugin_hooks", "plugin_permissions"} {
		var n int
		if err := d.QueryRow(`SELECT COUNT(*) FROM `+tbl+` WHERE plugin_id = ?`, "com.t.del").Scan(&n); err != nil {
			t.Fatalf("count %s: %v", tbl, err)
		}
		if n != 0 {
			t.Fatalf("%s: expected cascade delete, still has %d rows", tbl, n)
		}
	}
	// plugin_storage has no FK to plugins -- must survive until DeleteStorage
	// is called explicitly (see UninstallPlugin's separate step).
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM plugin_storage WHERE plugin_id = ?`, "com.t.del").Scan(&n); err != nil {
		t.Fatalf("count plugin_storage: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected plugin_storage to survive plugin deletion (no FK), got %d rows", n)
	}
}

func TestPluginRepo_UpdatePluginTrust(t *testing.T) {
	ctx := context.Background()
	d := openMigratedDB(t, "till.db")
	repo := NewPluginRepo(d.DB)
	seedCatalogAndPlugin(t, ctx, repo, "com.t.trust", "1.0.0")

	if err := repo.UpdatePluginTrust(ctx, nil, "com.t.trust", "trusted"); err != nil {
		t.Fatalf("UpdatePluginTrust: %v", err)
	}
	var trust string
	if err := d.QueryRow(`SELECT trust_level FROM plugins WHERE id = ?`, "com.t.trust").Scan(&trust); err != nil {
		t.Fatalf("read trust: %v", err)
	}
	if trust != "trusted" {
		t.Fatalf("trust_level = %q, want trusted", trust)
	}
}

func TestPluginRepo_InsertAudit(t *testing.T) {
	ctx := context.Background()
	d := openMigratedDB(t, "till.db")
	repo := NewPluginRepo(d.DB)
	ts := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)

	if err := repo.InsertAudit(ctx, nil, "plugin_install", "com.t.audit", map[string]any{"source": "manual"}, ts); err != nil {
		t.Fatalf("InsertAudit: %v", err)
	}
	var entityType, action, dataJSON string
	if err := d.QueryRow(`SELECT entity_type, action, data_json FROM audit_log WHERE entity_id = ?`, "com.t.audit").
		Scan(&entityType, &action, &dataJSON); err != nil {
		t.Fatalf("read audit row: %v", err)
	}
	if entityType != "plugin" || action != "plugin_install" {
		t.Fatalf("unexpected audit row: entity_type=%q action=%q", entityType, action)
	}
	var payload map[string]string
	if err := json.Unmarshal([]byte(dataJSON), &payload); err != nil || payload["source"] != "manual" {
		t.Fatalf("payload not round-tripped: %q err=%v", dataJSON, err)
	}
}

func TestPluginRepo_InsertAuditRaw_CustomEntityType(t *testing.T) {
	ctx := context.Background()
	d := openMigratedDB(t, "till.db")
	repo := NewPluginRepo(d.DB)
	ts := time.Now()

	if err := repo.InsertAuditRaw(ctx, nil, "permission_denied", "plugin_permission", "com.t.raw", map[string]any{"permission": "storage"}, ts); err != nil {
		t.Fatalf("InsertAuditRaw: %v", err)
	}
	var entityType string
	if err := d.QueryRow(`SELECT entity_type FROM audit_log WHERE entity_id = ?`, "com.t.raw").Scan(&entityType); err != nil {
		t.Fatalf("read: %v", err)
	}
	if entityType != "plugin_permission" {
		t.Fatalf("entity_type = %q, want the custom type, not the hardcoded 'plugin'", entityType)
	}
}

func TestPluginRepo_ReplacePluginEntries(t *testing.T) {
	ctx := context.Background()
	d := openMigratedDB(t, "till.db")
	repo := NewPluginRepo(d.DB)
	seedCatalogAndPlugin(t, ctx, repo, "com.t.entries", "1.0.0")

	if err := repo.ReplacePluginEntries(ctx, nil, "com.t.entries", []PluginEntryRow{
		{Type: "page", Key: "home", Label: "Home"},
		{Type: "button", Key: "tender", Label: "Tender"},
	}); err != nil {
		t.Fatalf("first replace: %v", err)
	}
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM plugin_entries WHERE plugin_id = ?`, "com.t.entries").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 entries, got %d", n)
	}

	// Replacing again with a different set must remove the old rows, not
	// accumulate them.
	if err := repo.ReplacePluginEntries(ctx, nil, "com.t.entries", []PluginEntryRow{
		{Type: "page", Key: "settings", Label: "Settings"},
	}); err != nil {
		t.Fatalf("second replace: %v", err)
	}
	if err := d.QueryRow(`SELECT COUNT(*) FROM plugin_entries WHERE plugin_id = ?`, "com.t.entries").Scan(&n); err != nil {
		t.Fatalf("count 2: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 entry after replace, got %d (old rows not cleared)", n)
	}
	var key string
	if err := d.QueryRow(`SELECT key FROM plugin_entries WHERE plugin_id = ?`, "com.t.entries").Scan(&key); err != nil || key != "settings" {
		t.Fatalf("unexpected surviving entry: key=%q err=%v", key, err)
	}

	// Replacing with an empty slice clears entries entirely (no-op path after delete).
	if err := repo.ReplacePluginEntries(ctx, nil, "com.t.entries", nil); err != nil {
		t.Fatalf("clear replace: %v", err)
	}
	if err := d.QueryRow(`SELECT COUNT(*) FROM plugin_entries WHERE plugin_id = ?`, "com.t.entries").Scan(&n); err != nil {
		t.Fatalf("count 3: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 entries after clearing, got %d", n)
	}
}

func TestPluginRepo_InsertPluginHooks(t *testing.T) {
	ctx := context.Background()
	d := openMigratedDB(t, "till.db")
	repo := NewPluginRepo(d.DB)
	seedCatalogAndPlugin(t, ctx, repo, "com.t.hooks", "1.0.0")

	// No-op on empty slice.
	if err := repo.InsertPluginHooks(ctx, nil, nil); err != nil {
		t.Fatalf("empty hooks: %v", err)
	}

	if err := repo.InsertPluginHooks(ctx, nil, []PluginHookRow{
		{PluginID: "com.t.hooks", Event: "sale.completed", Action: "loyalty.award", Priority: 100, IsActive: true, ConfigJSON: "{}"},
	}); err != nil {
		t.Fatalf("insert hook: %v", err)
	}
	events, err := repo.ListPluginHookEvents(ctx, "com.t.hooks")
	if err != nil || len(events) != 1 || events[0] != "sale.completed" {
		t.Fatalf("ListPluginHookEvents after insert: %v err=%v", events, err)
	}
	active, err := repo.HasActiveHook(ctx, "com.t.hooks", "sale.completed")
	if err != nil || !active {
		t.Fatalf("HasActiveHook: active=%v err=%v", active, err)
	}

	// Re-upserting the same (plugin,event,action) updates priority/config
	// instead of erroring or duplicating (ON CONFLICT DO UPDATE).
	if err := repo.InsertPluginHooks(ctx, nil, []PluginHookRow{
		{PluginID: "com.t.hooks", Event: "sale.completed", Action: "loyalty.award", Priority: 50, IsActive: true, ConfigJSON: `{"x":1}`},
	}); err != nil {
		t.Fatalf("upsert hook: %v", err)
	}
	var n, priority int
	var cfg string
	if err := d.QueryRow(`SELECT COUNT(*) FROM plugin_hooks WHERE plugin_id=? AND event=? AND action=?`, "com.t.hooks", "sale.completed", "loyalty.award").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected upsert not duplicate, got %d rows", n)
	}
	if err := d.QueryRow(`SELECT priority, config_json FROM plugin_hooks WHERE plugin_id=? AND event=? AND action=?`, "com.t.hooks", "sale.completed", "loyalty.award").Scan(&priority, &cfg); err != nil {
		t.Fatalf("read: %v", err)
	}
	if priority != 50 || cfg != `{"x":1}` {
		t.Fatalf("upsert did not update priority/config: priority=%d cfg=%q", priority, cfg)
	}

	active, err = repo.HasActiveHook(ctx, "com.t.hooks", "sale.voided")
	if err != nil || active {
		t.Fatalf("HasActiveHook for an unrelated event should be false, got active=%v err=%v", active, err)
	}
}

func TestPluginRepo_InsertPluginPermissions(t *testing.T) {
	ctx := context.Background()
	d := openMigratedDB(t, "till.db")
	repo := NewPluginRepo(d.DB)
	seedCatalogAndPlugin(t, ctx, repo, "com.t.perms", "1.0.0")

	// No-op on empty slice, and blank permission strings are skipped.
	if err := repo.InsertPluginPermissions(ctx, nil, "com.t.perms", nil); err != nil {
		t.Fatalf("empty: %v", err)
	}
	if err := repo.InsertPluginPermissions(ctx, nil, "com.t.perms", []string{"storage", "", "devices:printer"}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM plugin_permissions WHERE plugin_id = ?`, "com.t.perms").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 permission rows (blank skipped), got %d", n)
	}
	granted, exists, err := repo.CheckPermission(ctx, "com.t.perms", "storage")
	if err != nil || !exists || granted {
		t.Fatalf("newly declared permission should exist but default to not granted: granted=%v exists=%v err=%v", granted, exists, err)
	}

	// Operator grants it, then a manifest re-apply (upgrade) must NOT reset
	// the grant back to 0 -- ON CONFLICT DO NOTHING is the security-relevant
	// contract here.
	if err := repo.SetPermission(ctx, "com.t.perms", "storage", true); err != nil {
		t.Fatalf("SetPermission: %v", err)
	}
	if err := repo.InsertPluginPermissions(ctx, nil, "com.t.perms", []string{"storage", "devices:printer"}); err != nil {
		t.Fatalf("re-declare on upgrade: %v", err)
	}
	granted, exists, err = repo.CheckPermission(ctx, "com.t.perms", "storage")
	if err != nil || !exists || !granted {
		t.Fatalf("re-declaring an already-granted permission must not revoke it: granted=%v exists=%v err=%v", granted, exists, err)
	}
}

func TestPluginRepo_ListAutoStartPlugins(t *testing.T) {
	ctx := context.Background()
	d := openMigratedDB(t, "till.db")
	repo := NewPluginRepo(d.DB)
	seedCatalogAndPlugin(t, ctx, repo, "com.t.auto1", "1.0.0")
	seedCatalogAndPlugin(t, ctx, repo, "com.t.auto2", "1.0.0")

	// auto2 is disabled -- must not appear.
	if err := repo.SetPluginActive(ctx, nil, "com.t.auto2", false); err != nil {
		t.Fatalf("disable auto2: %v", err)
	}
	// auto3 is active but not in 'installed' state (e.g. mid-uninstall) --
	// must also be excluded.
	if err := repo.EnsureCatalogEntry(ctx, nil, CatalogUpsertRow{
		ID: "com.t.auto3", Version: "1.0.0", Name: "P3", Runtime: "wasm", Entrypoint: "p.wasm",
		PackageURL: "u", SHA256: "s", MinPOSVersion: "0.1.0", PublishedAt: "2026-07-30",
	}); err != nil {
		t.Fatalf("seed catalog3: %v", err)
	}
	now := time.Now().UTC()
	if err := repo.UpsertPluginManifest(ctx, nil, ManifestRow{
		ID: "com.t.auto3", Name: "P3", Version: "1.0.0", InstallState: "uninstalling",
		Entrypoint: "p.wasm", Runtime: "wasm", TrustLevel: "untrusted", InstalledAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed plugin3: %v", err)
	}

	rows, err := repo.ListAutoStartPlugins(ctx)
	if err != nil {
		t.Fatalf("ListAutoStartPlugins: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != "com.t.auto1" {
		t.Fatalf("expected only com.t.auto1, got %+v", rows)
	}
}

func TestPluginRepo_ListManagedPlugins_IncludesDisabled(t *testing.T) {
	ctx := context.Background()
	d := openMigratedDB(t, "till.db")
	repo := NewPluginRepo(d.DB)
	seedCatalogAndPlugin(t, ctx, repo, "com.t.mgd1", "1.0.0")
	seedCatalogAndPlugin(t, ctx, repo, "com.t.mgd2", "1.0.0")
	if err := repo.SetPluginActive(ctx, nil, "com.t.mgd2", false); err != nil {
		t.Fatalf("disable mgd2: %v", err)
	}

	rows, err := repo.ListManagedPlugins(ctx)
	if err != nil {
		t.Fatalf("ListManagedPlugins: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected both plugins (incl. disabled), got %d: %+v", len(rows), rows)
	}
	var sawDisabled bool
	for _, r := range rows {
		if r.ID == "com.t.mgd2" {
			sawDisabled = true
			if r.IsActive {
				t.Fatalf("mgd2 should be reported inactive: %+v", r)
			}
		}
	}
	if !sawDisabled {
		t.Fatal("disabled plugin missing from ListManagedPlugins -- management page couldn't re-enable it")
	}
}
