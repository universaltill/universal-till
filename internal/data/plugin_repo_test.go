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
		`CREATE TABLE plugins (id TEXT PRIMARY KEY, name TEXT, version TEXT, author TEXT, is_active INTEGER NOT NULL DEFAULT 1, install_state TEXT DEFAULT 'installed', runtime TEXT DEFAULT 'go', entrypoint TEXT DEFAULT '');`,
		`CREATE TABLE plugin_entries (id TEXT PRIMARY KEY, plugin_id TEXT NOT NULL, type TEXT, key TEXT, route TEXT, label TEXT, menu_group TEXT, icon_path TEXT, parent_page_key TEXT, target_action TEXT, trigger_event TEXT, config_json TEXT, sort_order INTEGER DEFAULT 0, is_active INTEGER NOT NULL DEFAULT 1);`,
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

	if _, err := db.Exec(`INSERT INTO plugins(id,name,version,author,is_active,install_state) VALUES('active','Good','1.0','dev-1',1,'installed')`); err != nil {
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
	// The REAL author must round-trip: a hardcoded '' here silently killed the
	// update checker's author/name catalog matching (batch 5 bug #5).
	if rows[0].Author != "dev-1" {
		t.Fatalf("author = %q, want dev-1", rows[0].Author)
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

func TestPluginRepo_ListButtonEntries(t *testing.T) {
	ctx := context.Background()
	db := newPluginRepoTestDB(t)
	repo := NewPluginRepo(db)

	if _, err := db.Exec(`INSERT INTO plugins(id,name,version,is_active) VALUES('b1','Stripe','1.2.0',1)`); err != nil {
		t.Fatalf("seed plugin: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO plugin_entries(id,plugin_id,type,key,label,icon_path,sort_order,is_active) VALUES('be1','b1','button','tender','Card','icons/card.svg',1,1)`); err != nil {
		t.Fatalf("seed button entry: %v", err)
	}
	// Inactive plugin: its button must be filtered out.
	if _, err := db.Exec(`INSERT INTO plugins(id,name,version,is_active) VALUES('b2','Old','0.1',0)`); err != nil {
		t.Fatalf("seed plugin2: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO plugin_entries(id,plugin_id,type,key,label,is_active) VALUES('be2','b2','button','old','Old',1)`); err != nil {
		t.Fatalf("seed button entry2: %v", err)
	}
	// Non-button entry: excluded.
	if _, err := db.Exec(`INSERT INTO plugin_entries(id,plugin_id,type,key,label,is_active) VALUES('be3','b1','page','p','P',1)`); err != nil {
		t.Fatalf("seed page entry: %v", err)
	}

	rows, err := repo.ListButtonEntries(ctx)
	if err != nil {
		t.Fatalf("ListButtonEntries: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 button, got %d: %+v", len(rows), rows)
	}
	r := rows[0]
	if r.PluginID != "b1" || r.PluginVersion != "1.2.0" || r.EntryKey != "tender" ||
		r.Label != "Card" || r.IconPath != "icons/card.svg" {
		t.Fatalf("unexpected row: %+v", r)
	}
}

func TestPluginRepo_ListExportEntries(t *testing.T) {
	ctx := context.Background()
	db := newPluginRepoTestDB(t)
	repo := NewPluginRepo(db)

	if _, err := db.Exec(`INSERT INTO plugins(id,name,version,is_active) VALUES('none','No Entries','1.0',1)`); err != nil {
		t.Fatalf("seed plugin: %v", err)
	}
	if rows, err := repo.ListExportEntries(ctx); err != nil || len(rows) != 0 {
		t.Fatalf("expected no export entries when none installed, got %+v err=%v", rows, err)
	}

	// One 'export'-type entry and one 'report'-type entry, on different
	// active plugins — both must be returned.
	if _, err := db.Exec(`INSERT INTO plugins(id,name,version,is_active) VALUES('e1','Excel Exporter','1.0',1)`); err != nil {
		t.Fatalf("seed plugin e1: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO plugin_entries(id,plugin_id,type,key,label,sort_order,is_active) VALUES('ee1','e1','export','csv_export','CSV Export',1,1)`); err != nil {
		t.Fatalf("seed export entry: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO plugins(id,name,version,is_active) VALUES('r1','DSFinV-K Report','1.0',1)`); err != nil {
		t.Fatalf("seed plugin r1: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO plugin_entries(id,plugin_id,type,key,label,sort_order,is_active) VALUES('re1','r1','report','dsfinvk','DSFinV-K Report',2,1)`); err != nil {
		t.Fatalf("seed report entry: %v", err)
	}
	// An entry on an inactive plugin must be excluded.
	if _, err := db.Exec(`INSERT INTO plugins(id,name,version,is_active) VALUES('e2','Old Exporter','0.1',0)`); err != nil {
		t.Fatalf("seed plugin e2: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO plugin_entries(id,plugin_id,type,key,label,sort_order,is_active) VALUES('ee2','e2','export','old_export','Old Export',3,1)`); err != nil {
		t.Fatalf("seed export entry e2: %v", err)
	}
	// A non-export/report entry must be excluded too.
	if _, err := db.Exec(`INSERT INTO plugin_entries(id,plugin_id,type,key,label,is_active) VALUES('pe1','e1','page','p','P',1)`); err != nil {
		t.Fatalf("seed page entry: %v", err)
	}

	rows, err := repo.ListExportEntries(ctx)
	if err != nil {
		t.Fatalf("ListExportEntries: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 export/report entries, got %d: %+v", len(rows), rows)
	}
	byKey := map[string]ExportEntryRow{}
	for _, r := range rows {
		byKey[r.Key] = r
	}
	csvRow, ok := byKey["csv_export"]
	if !ok || csvRow.PluginID != "e1" || csvRow.Label != "CSV Export" || csvRow.SortOrder != 1 {
		t.Fatalf("unexpected csv_export row: %+v (present=%v)", csvRow, ok)
	}
	reportRow, ok := byKey["dsfinvk"]
	if !ok || reportRow.PluginID != "r1" || reportRow.Label != "DSFinV-K Report" || reportRow.SortOrder != 2 {
		t.Fatalf("unexpected dsfinvk row: %+v (present=%v)", reportRow, ok)
	}
	if _, ok := byKey["old_export"]; ok {
		t.Fatalf("expected the inactive plugin's export entry excluded, got %+v", rows)
	}
}

// ListImportEntries mirrors ListExportEntries for type='import' rows
// (ut-docs#599), additionally unpacking the entities/file_formats
// declarations PersistManifest folds into config_json.
func TestPluginRepo_ListImportEntries(t *testing.T) {
	ctx := context.Background()
	db := newPluginRepoTestDB(t)
	repo := NewPluginRepo(db)

	if rows, err := repo.ListImportEntries(ctx); err != nil || len(rows) != 0 {
		t.Fatalf("expected no import entries when none installed, got %+v err=%v", rows, err)
	}

	if _, err := db.Exec(`INSERT INTO plugins(id,name,version,is_active) VALUES('i1','Speedy Importer','1.0',1)`); err != nil {
		t.Fatalf("seed plugin i1: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO plugin_entries(id,plugin_id,type,key,label,sort_order,is_active,config_json)
	                      VALUES('ie1','i1','import','bkp_import','Speedy .bkp Import',1,1,
	                             '{"entities":["items","categories"],"file_formats":[".bkp"]}')`); err != nil {
		t.Fatalf("seed import entry: %v", err)
	}
	// No config_json at all: still listed, just with no declarations.
	if _, err := db.Exec(`INSERT INTO plugins(id,name,version,is_active) VALUES('i2','Bare Importer','1.0',1)`); err != nil {
		t.Fatalf("seed plugin i2: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO plugin_entries(id,plugin_id,type,key,label,sort_order,is_active)
	                      VALUES('ie2','i2','import','bare_import','Bare Import',2,1)`); err != nil {
		t.Fatalf("seed bare import entry: %v", err)
	}
	// An entry on an inactive plugin must be excluded.
	if _, err := db.Exec(`INSERT INTO plugins(id,name,version,is_active) VALUES('i3','Old Importer','0.1',0)`); err != nil {
		t.Fatalf("seed plugin i3: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO plugin_entries(id,plugin_id,type,key,label,is_active) VALUES('ie3','i3','import','old_import','Old',1)`); err != nil {
		t.Fatalf("seed old import entry: %v", err)
	}
	// A non-import entry must be excluded too.
	if _, err := db.Exec(`INSERT INTO plugin_entries(id,plugin_id,type,key,label,is_active) VALUES('ee9','i1','export','exp','Exp',1)`); err != nil {
		t.Fatalf("seed export entry: %v", err)
	}

	rows, err := repo.ListImportEntries(ctx)
	if err != nil {
		t.Fatalf("ListImportEntries: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 import entries, got %d: %+v", len(rows), rows)
	}
	byKey := map[string]ImportEntryRow{}
	for _, r := range rows {
		byKey[r.Key] = r
	}
	bkp, ok := byKey["bkp_import"]
	if !ok || bkp.PluginID != "i1" || bkp.Label != "Speedy .bkp Import" || bkp.SortOrder != 1 {
		t.Fatalf("unexpected bkp_import row: %+v (present=%v)", bkp, ok)
	}
	if len(bkp.Entities) != 2 || bkp.Entities[0] != "items" || bkp.Entities[1] != "categories" {
		t.Fatalf("unexpected entities: %+v", bkp.Entities)
	}
	if len(bkp.FileFormats) != 1 || bkp.FileFormats[0] != ".bkp" {
		t.Fatalf("unexpected file_formats: %+v", bkp.FileFormats)
	}
	bare, ok := byKey["bare_import"]
	if !ok || bare.PluginID != "i2" || len(bare.Entities) != 0 || len(bare.FileFormats) != 0 {
		t.Fatalf("unexpected bare_import row: %+v (present=%v)", bare, ok)
	}
	if _, ok := byKey["old_import"]; ok {
		t.Fatalf("expected the inactive plugin's import entry excluded, got %+v", rows)
	}
}

// TestPluginRepo_ListExportEntries_Entities is ListExportEntries' ut-docs#600
// counterpart to TestPluginRepo_ListImportEntries above: export entries can
// now declare Entities (config_json, same column/shape import entries
// already use) so the /api/data/export dispatcher can gate a catalog-entity
// ledger (e.g. "items") on both a declared entity AND a permission grant,
// mirroring how ImportEntryRow.Entities already resolves an import's
// declared set. Structurally mirrors TestPluginRepo_ListImportEntries:
// a declared-entities row, a bare (no config_json) row, an inactive-plugin
// row excluded, and a non-export-type row excluded.
func TestPluginRepo_ListExportEntries_Entities(t *testing.T) {
	ctx := context.Background()
	db := newPluginRepoTestDB(t)
	repo := NewPluginRepo(db)

	if _, err := db.Exec(`INSERT INTO plugins(id,name,version,is_active) VALUES('e1','Catalog Exporter','1.0',1)`); err != nil {
		t.Fatalf("seed plugin e1: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO plugin_entries(id,plugin_id,type,key,label,sort_order,is_active,config_json)
	                      VALUES('ee1','e1','export','catalog_export','Catalog Export',1,1,
	                             '{"entities":["items"]}')`); err != nil {
		t.Fatalf("seed export entry: %v", err)
	}
	// No config_json at all: still listed, just with no declarations —
	// today's Sales/Stock-only entries (pre-ut-docs#600) look like this.
	if _, err := db.Exec(`INSERT INTO plugins(id,name,version,is_active) VALUES('e2','Bare Exporter','1.0',1)`); err != nil {
		t.Fatalf("seed plugin e2: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO plugin_entries(id,plugin_id,type,key,label,sort_order,is_active)
	                      VALUES('ee2','e2','export','bare_export','Bare Export',2,1)`); err != nil {
		t.Fatalf("seed bare export entry: %v", err)
	}
	// An entry on an inactive plugin must be excluded.
	if _, err := db.Exec(`INSERT INTO plugins(id,name,version,is_active) VALUES('e3','Old Exporter','0.1',0)`); err != nil {
		t.Fatalf("seed plugin e3: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO plugin_entries(id,plugin_id,type,key,label,is_active) VALUES('ee3','e3','export','old_export','Old',1)`); err != nil {
		t.Fatalf("seed old export entry: %v", err)
	}
	// A non-export/report entry must be excluded too.
	if _, err := db.Exec(`INSERT INTO plugin_entries(id,plugin_id,type,key,label,is_active) VALUES('ie9','e1','import','imp','Imp',1)`); err != nil {
		t.Fatalf("seed import entry: %v", err)
	}

	rows, err := repo.ListExportEntries(ctx)
	if err != nil {
		t.Fatalf("ListExportEntries: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 export entries, got %d: %+v", len(rows), rows)
	}
	byKey := map[string]ExportEntryRow{}
	for _, r := range rows {
		byKey[r.Key] = r
	}
	catalog, ok := byKey["catalog_export"]
	if !ok || catalog.PluginID != "e1" || catalog.Label != "Catalog Export" || catalog.SortOrder != 1 {
		t.Fatalf("unexpected catalog_export row: %+v (present=%v)", catalog, ok)
	}
	if len(catalog.Entities) != 1 || catalog.Entities[0] != "items" {
		t.Fatalf("unexpected entities: %+v", catalog.Entities)
	}
	bare, ok := byKey["bare_export"]
	if !ok || bare.PluginID != "e2" || len(bare.Entities) != 0 {
		t.Fatalf("unexpected bare_export row: %+v (present=%v)", bare, ok)
	}
	if _, ok := byKey["old_export"]; ok {
		t.Fatalf("expected the inactive plugin's export entry excluded, got %+v", rows)
	}
}

// TestPluginRepo_ListExportEntries_MalformedConfigDegradesGracefully is the
// ut-docs#600 review's N2 case: a hand-edited/legacy config_json that isn't
// valid JSON must degrade to an empty Entities on that one row, per
// ListExportEntries' own doc comment — not fail the whole listing.
func TestPluginRepo_ListExportEntries_MalformedConfigDegradesGracefully(t *testing.T) {
	ctx := context.Background()
	db := newPluginRepoTestDB(t)
	repo := NewPluginRepo(db)

	if _, err := db.Exec(`INSERT INTO plugins(id,name,version,is_active) VALUES('e4','Broken Exporter','1.0',1)`); err != nil {
		t.Fatalf("seed plugin: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO plugin_entries(id,plugin_id,type,key,label,sort_order,is_active,config_json)
	                      VALUES('ee4','e4','export','broken_export','Broken',1,1,'{not valid json')`); err != nil {
		t.Fatalf("seed export entry: %v", err)
	}

	rows, err := repo.ListExportEntries(ctx)
	if err != nil {
		t.Fatalf("ListExportEntries: %v", err)
	}
	if len(rows) != 1 || rows[0].Key != "broken_export" || len(rows[0].Entities) != 0 {
		t.Fatalf("expected 1 row with empty Entities despite malformed config_json, got %+v", rows)
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
