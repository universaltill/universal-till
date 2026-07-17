package data

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func newPluginRepoTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	stmts := []string{
		`PRAGMA foreign_keys = ON;`,
		`CREATE TABLE plugins (id TEXT PRIMARY KEY, name TEXT, version TEXT, is_active INTEGER NOT NULL DEFAULT 1, install_state TEXT DEFAULT 'installed', runtime TEXT DEFAULT 'go', entrypoint TEXT DEFAULT '');`,
		`CREATE TABLE plugin_entries (id TEXT PRIMARY KEY, plugin_id TEXT NOT NULL, type TEXT, key TEXT, route TEXT, label TEXT, menu_group TEXT, config_json TEXT, sort_order INTEGER DEFAULT 0, is_active INTEGER NOT NULL DEFAULT 1);`,
		`CREATE TABLE plugin_permissions (id TEXT PRIMARY KEY, plugin_id TEXT NOT NULL, permission TEXT NOT NULL, granted INTEGER NOT NULL DEFAULT 0);`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("setup plugin schema: %v", err)
		}
	}
	return db
}

func TestPluginRepo_ListMenuEntries_FiltersUngranted(t *testing.T) {
	ctx := context.Background()
	db := newPluginRepoTestDB(t)
	repo := NewPluginRepo(db)

	// p1 has a granted permission, should be returned.
	if _, err := db.Exec(`INSERT INTO plugins(id,name,version,is_active) VALUES('p1','Plugin One','1.0',1)`); err != nil {
		t.Fatalf("seed plugin: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO plugin_permissions(id,plugin_id,permission,granted) VALUES('pp1','p1','menus:view',1)`); err != nil {
		t.Fatalf("seed permission: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO plugin_entries(id,plugin_id,type,key,route,label,menu_group,sort_order,is_active) VALUES('pe1','p1','page','home','/home','Home','main',1,1)`); err != nil {
		t.Fatalf("seed entry: %v", err)
	}

	// p2 has permission but not granted, should be filtered out.
	if _, err := db.Exec(`INSERT INTO plugins(id,name,version,is_active) VALUES('p2','Plugin Two','1.0',1)`); err != nil {
		t.Fatalf("seed plugin2: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO plugin_permissions(id,plugin_id,permission,granted) VALUES('pp2','p2','menus:view',0)`); err != nil {
		t.Fatalf("seed permission2: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO plugin_entries(id,plugin_id,type,key,route,label,menu_group,sort_order,is_active) VALUES('pe2','p2','page','settings','/settings','Settings','main',2,1)`); err != nil {
		t.Fatalf("seed entry2: %v", err)
	}

	rows, err := repo.ListMenuEntries(ctx)
	if err != nil {
		t.Fatalf("ListMenuEntries: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 menu entries, got %d", len(rows))
	}
	// Ensure granted flags flow through so callers can filter.
	for _, row := range rows {
		if row.PluginID == "p1" && (!row.GrantedFlags.Valid || row.GrantedFlags.String != "1") {
			t.Fatalf("expected granted flag for p1, got %+v", row.GrantedFlags)
		}
		if row.PluginID == "p2" && (!row.GrantedFlags.Valid || row.GrantedFlags.String != "0") {
			t.Fatalf("expected denied flag for p2, got %+v", row.GrantedFlags)
		}
	}
}

func TestPluginRepo_ListInstalledPlugins_ActiveOnly(t *testing.T) {
	ctx := context.Background()
	db := newPluginRepoTestDB(t)
	repo := NewPluginRepo(db)

	if _, err := db.Exec(`INSERT INTO plugins(id,name,version,is_active,install_state) VALUES('active','Good','1.0',1,'installed')`); err != nil {
		t.Fatalf("seed active: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO plugins(id,name,version,is_active,install_state) VALUES('inactive','Bad','1.0',0,'installed')`); err != nil {
		t.Fatalf("seed inactive: %v", err)
	}

	rows, err := repo.ListInstalledPlugins(ctx)
	if err != nil {
		t.Fatalf("ListInstalledPlugins: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != "active" {
		t.Fatalf("expected only active plugin, got %+v", rows)
	}
}

func TestPluginRepo_ListThemeEntries(t *testing.T) {
	ctx := context.Background()
	db := newPluginRepoTestDB(t)
	repo := NewPluginRepo(db)

	if _, err := db.Exec(`INSERT INTO plugins(id,name,version,is_active) VALUES('t1','Midnight','1.0.0',1)`); err != nil {
		t.Fatalf("seed plugin: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO plugin_entries(id,plugin_id,type,key,label,config_json,is_active) VALUES('te1','t1','theme','midnight','Midnight (dark)','{"css":"assets/theme.css"}',1)`); err != nil {
		t.Fatalf("seed theme entry: %v", err)
	}
	// Inactive plugin: theme must be filtered out.
	if _, err := db.Exec(`INSERT INTO plugins(id,name,version,is_active) VALUES('t2','Old','0.1',0)`); err != nil {
		t.Fatalf("seed plugin2: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO plugin_entries(id,plugin_id,type,key,label,is_active) VALUES('te2','t2','theme','old','Old',1)`); err != nil {
		t.Fatalf("seed theme entry2: %v", err)
	}
	// Non-theme entry: excluded.
	if _, err := db.Exec(`INSERT INTO plugin_entries(id,plugin_id,type,key,label,is_active) VALUES('te3','t1','page','p','P',1)`); err != nil {
		t.Fatalf("seed page entry: %v", err)
	}

	rows, err := repo.ListThemeEntries(ctx)
	if err != nil {
		t.Fatalf("ListThemeEntries: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 theme, got %d: %+v", len(rows), rows)
	}
	r := rows[0]
	if r.PluginID != "t1" || r.PluginVersion != "1.0.0" || r.EntryKey != "midnight" ||
		r.Label != "Midnight (dark)" || r.ConfigJSON != `{"css":"assets/theme.css"}` {
		t.Fatalf("unexpected row: %+v", r)
	}
}

// Upgrade path for plugin settings: values the operator configured must
// survive a manifest re-apply, dupes from the old NULL-scope_id upsert must
// collapse, a scope change in the manifest must move the row (keeping its
// value), and undeclared keys must go. Uses the real migrated schema.
func TestReconcilePluginSettingsUpgrade(t *testing.T) {
	ctx := context.Background()
	d := openMigratedDB(t, "till.db")
	mustExec(t, d, `INSERT INTO plugin_catalog (id, version, name, runtime, entrypoint, package_url, sha256, min_pos_version, api_version, published_at) VALUES ('com.t.p', '1.0.0', 'P', 'wasm', 'p.wasm', 'u', 's', '0.1.0', '1', '2026-07-17')`)
	mustExec(t, d, `INSERT INTO plugins (id, name, version, entrypoint, runtime) VALUES ('com.t.p', 'P', '1.0.0', 'p.wasm', 'wasm')`)
	repo := NewPluginRepo(d.DB)

	decl := func(key, def, scope string) PluginSettingRow {
		return PluginSettingRow{PluginID: "com.t.p", Key: key, ValueJSON: def, Scope: scope, UpdatedAt: time.Now()}
	}

	// v1 installs: reader + secret + soon-to-be-dropped key, all global.
	v1 := []PluginSettingRow{decl("reader_id", `""`, "global"), decl("secret_key", `""`, "global"), decl("old_flag", `"off"`, "global")}
	if err := repo.ReconcilePluginSettings(ctx, nil, "com.t.p", v1); err != nil {
		t.Fatalf("reconcile v1: %v", err)
	}

	// Operator configures both, then the old upsert bug leaves a duplicate
	// default row for secret_key (scope_id NULL never conflicted).
	if err := repo.UpsertPluginSetting(ctx, "com.t.p", "reader_id", `"tmr_1"`); err != nil {
		t.Fatalf("set reader: %v", err)
	}
	if err := repo.UpsertPluginSetting(ctx, "com.t.p", "secret_key", `"sk_live"`); err != nil {
		t.Fatalf("set secret: %v", err)
	}
	mustExec(t, d, `INSERT INTO plugin_settings (id, plugin_id, key, value_json, scope) VALUES ('dupe', 'com.t.p', 'secret_key', '""', 'global')`)

	// v2 upgrade: reader becomes per-till (register), old_flag dropped, a new
	// key appears.
	v2 := []PluginSettingRow{decl("reader_id", `""`, "register"), decl("secret_key", `""`, "global"), decl("new_mode", `"auto"`, "global")}
	if err := repo.ReconcilePluginSettings(ctx, nil, "com.t.p", v2); err != nil {
		t.Fatalf("reconcile v2: %v", err)
	}

	var n int
	var v, scope string
	if err := d.QueryRow(`SELECT COUNT(*), value_json, scope FROM plugin_settings WHERE plugin_id = 'com.t.p' AND key = 'reader_id'`).Scan(&n, &v, &scope); err != nil {
		t.Fatalf("reader row: %v", err)
	}
	if n != 1 || v != `"tmr_1"` || scope != "register" {
		t.Fatalf("reader after upgrade: n=%d v=%s scope=%s, want 1 row, value kept, scope moved", n, v, scope)
	}
	if err := d.QueryRow(`SELECT COUNT(*), value_json FROM plugin_settings WHERE plugin_id = 'com.t.p' AND key = 'secret_key'`).Scan(&n, &v); err != nil {
		t.Fatalf("secret row: %v", err)
	}
	if n != 1 || v != `"sk_live"` {
		t.Fatalf("secret after upgrade: n=%d v=%s, want the configured value, dupes collapsed", n, v)
	}
	_ = d.QueryRow(`SELECT COUNT(*) FROM plugin_settings WHERE plugin_id = 'com.t.p' AND key = 'old_flag'`).Scan(&n)
	if n != 0 {
		t.Fatal("undeclared key survived the upgrade")
	}
	if err := d.QueryRow(`SELECT value_json FROM plugin_settings WHERE plugin_id = 'com.t.p' AND key = 'new_mode'`).Scan(&v); err != nil || v != `"auto"` {
		t.Fatalf("new key default missing: %q %v", v, err)
	}

	// Scope-aware reads/writes: the register row shadows a global one, and a
	// scoped write targets its own row.
	if err := repo.UpsertPluginSettingScoped(ctx, "com.t.p", "reader_id", `"tmr_2"`, "register"); err != nil {
		t.Fatalf("scoped upsert: %v", err)
	}
	mustExec(t, d, `INSERT INTO plugin_settings (id, plugin_id, key, value_json, scope) VALUES ('glob', 'com.t.p', 'reader_id', '"tmr_shop"', 'global')`)
	got, found, err := repo.GetPluginSetting(ctx, "com.t.p", "reader_id")
	if err != nil || !found || got != `"tmr_2"` {
		t.Fatalf("GetPluginSetting = %q found=%v err=%v, want the register-scoped value", got, found, err)
	}
}
