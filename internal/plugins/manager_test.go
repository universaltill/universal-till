package plugins

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/paths"
)

// managerTestDB opens a real migrated DB (appdb.Open) — the Manager reads the
// real schema, not a hand-rolled one.
func managerTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := openMarketplaceInstallerDB(t)
	t.Cleanup(func() { db.Close() })
	return db
}

func seedCatalogRow(t *testing.T, db *sql.DB, id, name, version, tagsJSON string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO plugin_catalog (id, version, name, description, author, website, runtime, entrypoint, package_url, sha256, min_pos_version, api_version, tags_json, published_at)
		VALUES (?, ?, ?, 'demo', 'me', 'https://x', 'none', './run', 'https://x/p.tgz', 'abc', '0.0.1', '1.0', ?, datetime('now'))`, id, version, name, tagsJSON); err != nil {
		t.Fatalf("seed catalog %s: %v", id, err)
	}
}

func seedInstalledPlugin(t *testing.T, db *sql.DB, id, name, version, runtime string, active bool) {
	t.Helper()
	// plugins(id,version) FKs plugin_catalog(id,version) in the real schema.
	seedCatalogRow(t, db, id, name, version, "")
	activeInt := 0
	if active {
		activeInt = 1
	}
	if _, err := db.Exec(`INSERT INTO plugins (id, name, version, entrypoint, runtime, is_active, trust_level, installed_from_url, installed_sha256)
		VALUES (?, ?, ?, './run', ?, ?, 'trusted', 'file://x', 'x')`, id, name, version, runtime, activeInt); err != nil {
		t.Fatalf("seed plugin %s: %v", id, err)
	}
}

func seedMenuEntry(t *testing.T, db *sql.DB, pluginID, key, label string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO plugin_entries (id, plugin_id, type, key, label, menu_group, route)
		VALUES (?, ?, 'page', ?, ?, 'main', '/x/'||?)`, pluginID+"-"+key, pluginID, key, label, key); err != nil {
		t.Fatalf("seed entry %s: %v", key, err)
	}
}

func TestManagerInitAndReload(t *testing.T) {
	db := managerTestDB(t)
	ctx := context.Background()

	seedInstalledPlugin(t, db, "com.test.a", "Plugin A", "1.0.0", "none", true)
	seedInstalledPlugin(t, db, "com.test.b", "Plugin B", "2.0.0", "none", true)
	// Menu entry for a plugin with NO declared permissions → allowed.
	seedMenuEntry(t, db, "com.test.a", "page-a", "Page A")

	// A catalog-only plugin (not installed), with tags to parse.
	seedCatalogRow(t, db, "com.test.c", "Plugin C", "1.0.0", `["pos","report"]`)

	m, err := Init(ctx, &config.Config{Env: "test"}, db)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	if got := m.InstalledIDs(); len(got) != 2 || got[0] != "com.test.a" || got[1] != "com.test.b" {
		t.Fatalf("InstalledIDs = %v", got)
	}
	if _, ok := m.MenuPlugins["page-a"]; !ok {
		t.Fatalf("menu entry for permissionless plugin missing: %+v", m.MenuPlugins)
	}

	views := m.CatalogList()
	if len(views) != 3 {
		t.Fatalf("CatalogList len = %d, want 3", len(views))
	}
	// Sorted by name; installed flag derived from Installed map.
	if views[0].ID != "com.test.a" || !views[0].Installed {
		t.Fatalf("catalog A view wrong: %+v", views[0])
	}
	if views[2].ID != "com.test.c" || views[2].Installed {
		t.Fatalf("catalog C view wrong: %+v", views[2])
	}
	if len(views[2].Tags) != 2 || views[2].Tags[0] != "pos" {
		t.Fatalf("tags not parsed: %+v", views[2].Tags)
	}

	// Uninstall B, Reload must drop it.
	if err := UninstallPlugin(ctx, db, "com.test.b"); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if err := m.Reload(ctx); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if got := m.InstalledIDs(); len(got) != 1 || got[0] != "com.test.a" {
		t.Fatalf("after reload InstalledIDs = %v", got)
	}
}

func TestLoadMenuEntriesPermissionGating(t *testing.T) {
	db := managerTestDB(t)
	ctx := context.Background()

	// granted: declares a permission and it is granted → entry shows.
	seedInstalledPlugin(t, db, "com.test.granted", "Granted", "1.0.0", "none", true)
	seedMenuEntry(t, db, "com.test.granted", "page-granted", "Granted Page")
	if _, err := db.Exec(`INSERT INTO plugin_permissions (id, plugin_id, permission, granted) VALUES ('p1', 'com.test.granted', 'storage', 1)`); err != nil {
		t.Fatalf("seed permission: %v", err)
	}

	// denied: declares a permission, NOT granted → entry hidden.
	seedInstalledPlugin(t, db, "com.test.denied", "Denied", "1.0.0", "none", true)
	seedMenuEntry(t, db, "com.test.denied", "page-denied", "Denied Page")
	if _, err := db.Exec(`INSERT INTO plugin_permissions (id, plugin_id, permission, granted) VALUES ('p2', 'com.test.denied', 'storage', 0)`); err != nil {
		t.Fatalf("seed permission: %v", err)
	}

	m, err := Init(ctx, &config.Config{Env: "test"}, db)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, ok := m.MenuPlugins["page-granted"]; !ok {
		t.Fatalf("granted plugin's entry hidden")
	}
	if _, ok := m.MenuPlugins["page-denied"]; ok {
		t.Fatalf("ungranted plugin's entry shown")
	}
}

func TestCatalogPage(t *testing.T) {
	db := managerTestDB(t)
	ctx := context.Background()

	seedCatalogRow(t, db, "com.test.p1", "Alpha", "1.0.0", `["pos"]`)
	seedCatalogRow(t, db, "com.test.p3", "Gamma", "1.0.0", `["pos"]`)
	// p2 is installed; its catalog row comes from seedInstalledPlugin. Tag it.
	seedInstalledPlugin(t, db, "com.test.p2", "Beta", "1.0.0", "none", true)
	if _, err := db.Exec(`UPDATE plugin_catalog SET tags_json = '["report"]' WHERE id = 'com.test.p2'`); err != nil {
		t.Fatalf("tag p2: %v", err)
	}

	m, err := Init(ctx, &config.Config{Env: "test"}, db)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	// Defaults kick in for bad offset/limit.
	all, total, err := m.CatalogPage(ctx, -5, 0, "")
	if err != nil {
		t.Fatalf("CatalogPage: %v", err)
	}
	if total != 3 || len(all) != 3 {
		t.Fatalf("total=%d len=%d", total, len(all))
	}
	for _, v := range all {
		if v.ID == "com.test.p2" && !v.Installed {
			t.Fatalf("installed flag lost for p2")
		}
	}

	// Tag filter.
	pos, total, err := m.CatalogPage(ctx, 0, 10, "pos")
	if err != nil {
		t.Fatalf("CatalogPage tag: %v", err)
	}
	if total != 2 || len(pos) != 2 {
		t.Fatalf("tag filter: total=%d len=%d", total, len(pos))
	}

	// Pagination.
	page2, _, err := m.CatalogPage(ctx, 2, 2, "")
	if err != nil {
		t.Fatalf("CatalogPage page2: %v", err)
	}
	if len(page2) != 1 {
		t.Fatalf("page2 len=%d", len(page2))
	}
}

func TestParseTags(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{`["a","b"]`, []string{"a", "b"}},
		{`[]`, nil},
		{``, nil},
		{`[" a ", ""]`, []string{"a"}},
		{`["one"]`, []string{"one"}},
	}
	for _, c := range cases {
		got := parseTags(c.in)
		if len(got) != len(c.want) {
			t.Errorf("parseTags(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("parseTags(%q)[%d] = %q, want %q", c.in, i, got[i], c.want[i])
			}
		}
	}
}

type captureLocalizer struct {
	overlays map[string]map[string]string
	calls    int
}

func (c *captureLocalizer) SetOverlays(o map[string]map[string]string) {
	c.overlays = o
	c.calls++
}

func TestSetLocalizerSyncsPluginLocales(t *testing.T) {
	// syncLocales reads locale files from paths.Plugins()/<id>/<version>/locales.
	dataDir := t.TempDir()
	prev := paths.DataDir()
	paths.Init(dataDir)
	t.Cleanup(func() { paths.Init(prev) })

	db := managerTestDB(t)
	ctx := context.Background()
	seedInstalledPlugin(t, db, "com.test.lang", "Lang Pack", "1.0.0", "none", true)
	seedInstalledPlugin(t, db, "com.test.nolocales", "No Locales", "1.0.0", "none", true)

	localeDir := filepath.Join(dataDir, "plugins", "com.test.lang", "1.0.0", "locales")
	if err := os.MkdirAll(localeDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localeDir, "de.json"), []byte(`{"greeting":"Hallo"}`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	// A bad locale file must be skipped, not break the sync.
	if err := os.WriteFile(filepath.Join(localeDir, "fr.json"), []byte(`{broken`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Non-json and directory entries are skipped.
	if err := os.WriteFile(filepath.Join(localeDir, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Mkdir(filepath.Join(localeDir, "sub.json"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	m, err := Init(ctx, &config.Config{Env: "test"}, db)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	loc := &captureLocalizer{}
	m.SetLocalizer(loc)
	if loc.calls != 1 {
		t.Fatalf("SetLocalizer did not sync immediately (calls=%d)", loc.calls)
	}
	if got := loc.overlays["de"]["greeting"]; got != "Hallo" {
		t.Fatalf("overlay missing: %+v", loc.overlays)
	}
	if _, ok := loc.overlays["fr"]; ok {
		t.Fatalf("broken locale file produced an overlay")
	}

	// Reload re-syncs into the same localizer.
	if err := m.Reload(ctx); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if loc.calls != 2 {
		t.Fatalf("Reload did not re-sync locales (calls=%d)", loc.calls)
	}
}

// ut-docs#16: an orphan-owned payment_methods row (plugin_id pointing at a
// plugin that isn't installed) must produce a startup warning, not fail
// Init — this is a log-only surface, not a hard error.
func TestManagerInit_ToleratesOrphanedPaymentMethod(t *testing.T) {
	db := managerTestDB(t)
	ctx := context.Background()

	if _, err := db.Exec(`INSERT INTO payment_methods (id, name, type, is_active, sort_order, plugin_id)
		VALUES ('orphaned', 'Orphaned Tender', 'card', 1, 100, 'com.gone.forever')`); err != nil {
		t.Fatalf("seed orphaned payment method: %v", err)
	}

	if _, err := Init(ctx, &config.Config{Env: "test"}, db); err != nil {
		t.Fatalf("Init must tolerate an orphaned payment method (log-only warning), got: %v", err)
	}
}
