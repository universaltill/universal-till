package db

import "testing"

// Migration 119: per-field ver on plugin_settings. Existing rows start at
// ver 1 origin till; the SumUp rows are seeded from the legacy plain counter
// sumup_key_version so an older replayed directive stays stale (ADR-0213).
func TestMigration119_PluginSettingsVerSeedsSumUpFromLegacyCounter(t *testing.T) {
	d, _ := openAtPreMigrationSchema(t, 119, "plugin-settings-ver.db")
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := d.DB.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for _, id := range []string{"sumup", "other"} {
		exec(`INSERT INTO plugin_catalog (id, version, name, description, author, website, runtime, entrypoint, package_url, sha256, min_pos_version, api_version, published_at, is_deprecated)
VALUES (?, '1.0.0', 'T', 'd', 'a', 's', 'wasm', 'e', 'https://example.com/p', 'dead', '0.1.0', '1', datetime('now'), 0)`, id)
	}
	exec(`INSERT INTO plugins (id, name, version, entrypoint, trust_level) VALUES ('sumup', 'SumUp', '1.0.0', 'x', 'official')`)
	exec(`INSERT INTO plugins (id, name, version, entrypoint, trust_level) VALUES ('other', 'Other', '1.0.0', 'x', 'official')`)
	set := func(id, plugin, key, val string) {
		exec(`INSERT INTO plugin_settings (id, plugin_id, key, value_json) VALUES (?, ?, ?, ?)`, id, plugin, key, val)
	}
	set("a", "sumup", "sumup_api_key", `"sealed"`)
	set("b", "sumup", "sumup_merchant_code", `"M1"`)
	set("c", "sumup", "sumup_key_version", `5`)
	set("d", "other", "sumup_api_key", `"x"`) // another plugin: untouched
	if err := d.applyMigration(loadMigrationVersion(t, 119)); err != nil {
		t.Fatalf("apply 119: %v", err)
	}
	row := func(id string) (ver int64, origin string) {
		t.Helper()
		if err := d.DB.QueryRow(`SELECT ver, origin FROM plugin_settings WHERE id = ?`, id).Scan(&ver, &origin); err != nil {
			t.Fatal(err)
		}
		return
	}
	for id, want := range map[string]int64{"a": 5, "b": 5} {
		if v, o := row(id); v != want || o != "cloud" {
			t.Errorf("row %s = ver %d origin %s, want %d cloud", id, v, o, want)
		}
	}
	for _, id := range []string{"c", "d"} {
		if v, o := row(id); v != 1 || o != "till" {
			t.Errorf("row %s = ver %d origin %s, want 1 till", id, v, o)
		}
	}
}
