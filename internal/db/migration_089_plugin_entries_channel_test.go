package db

import (
	"strings"
	"testing"
)

// 089 widens plugin_entries.type to admit `channel` (ADR-0169, ut-docs#4759)
// by rebuilding the table. The rebuild must keep every existing entry
// byte-for-byte (a child table: nothing may cascade away), keep the indexes,
// admit `channel`, and still refuse an unknown type.
func TestMigration089_PluginEntriesTypeChannel(t *testing.T) {
	const version = 89
	d, path := openAtPreMigrationSchema(t, version, "plugin-entries-channel.db")

	if _, err := d.DB.Exec(`INSERT INTO plugin_catalog (id, version, name, description, author, website, runtime, entrypoint, package_url, sha256, min_pos_version, api_version, tags_json, published_at)
		VALUES ('demo.p', '1.0.0', 'Demo', 'd', 'a', 'https://x', 'none', './run', 'https://x/p.tgz', 'abc', '0.0.1', '1.0', '[]', datetime('now'))`); err != nil {
		t.Fatalf("seed catalog: %v", err)
	}
	if _, err := d.DB.Exec(`INSERT INTO plugins (id, name, version, entrypoint, runtime) VALUES ('demo.p', 'Demo', '1.0.0', './run', 'none')`); err != nil {
		t.Fatalf("seed plugin: %v", err)
	}
	if _, err := d.DB.Exec(`INSERT INTO plugin_entries (id, plugin_id, type, key, label, route, config_json, sort_order)
		VALUES ('e1', 'demo.p', 'page', 'main', 'Main page', '/plugin/demo', '{"a":1}', 7)`); err != nil {
		t.Fatalf("seed entry: %v", err)
	}
	// Before 089 the CHECK refuses channel.
	if _, err := d.DB.Exec(`INSERT INTO plugin_entries (id, plugin_id, type, key, label) VALUES ('e0', 'demo.p', 'channel', 'c0', 'C')`); err == nil {
		t.Fatal("pre-089 schema should refuse type channel")
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open (applies 089): %v", err)
	}
	defer d.Close()

	var typ, label, route, cfg string
	var order int
	if err := d.DB.QueryRow(`SELECT type, label, route, config_json, sort_order FROM plugin_entries WHERE id='e1'`).Scan(&typ, &label, &route, &cfg, &order); err != nil {
		t.Fatalf("existing entry lost by the rebuild: %v", err)
	}
	if typ != "page" || label != "Main page" || route != "/plugin/demo" || cfg != `{"a":1}` || order != 7 {
		t.Fatalf("existing entry changed: %q %q %q %q %d", typ, label, route, cfg, order)
	}
	if _, err := d.DB.Exec(`INSERT INTO plugin_entries (id, plugin_id, type, key, label) VALUES ('e2', 'demo.p', 'channel', 'c1', 'Channel')`); err != nil {
		t.Fatalf("channel must be admitted after 089: %v", err)
	}
	if _, err := d.DB.Exec(`INSERT INTO plugin_entries (id, plugin_id, type, key, label) VALUES ('e3', 'demo.p', 'gadget', 'g1', 'G')`); err == nil {
		t.Fatal("an unknown type must still be refused")
	}
	rows, err := d.DB.Query(`SELECT name FROM sqlite_master WHERE type='index' AND tbl_name='plugin_entries' AND name LIKE 'idx_%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var idx []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		idx = append(idx, n)
	}
	for _, want := range []string{"idx_plugin_entries_plugin", "idx_plugin_entries_type", "idx_plugin_entries_parent_page"} {
		if !strings.Contains(strings.Join(idx, ","), want) {
			t.Fatalf("index %s missing after the rebuild: %v", want, idx)
		}
	}
}
