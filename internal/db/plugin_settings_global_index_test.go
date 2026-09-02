package db

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestUxPluginSettingsGlobalRejectsDuplicateRow confirms the schema-level
// backstop itself (the ux_plugin_settings_global unique index, ut-docs#787)
// still rejects a genuine duplicate global row at the DB level — moved here
// (ut-docs#807) from a sync-apply test now that the apply path
// (applyPluginSettings, internal/data/sync_admin_repo.go) defensively
// dedupes before it ever reaches this index, so a bundle carrying a
// stale-primary duplicate no longer exercises this constraint at all. The
// index itself must still exist and still work for any other writer that
// isn't going through the sync-apply path's dedupe.
func TestUxPluginSettingsGlobalRejectsDuplicateRow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ux-plugin-settings-global.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	if _, err := d.DB.Exec(`INSERT INTO plugin_catalog
		(id, version, name, runtime, entrypoint, package_url, sha256, min_pos_version, api_version, published_at)
		VALUES ('com.t.ux', '1.0.0', 'Ux Plugin', 'wasm', 'p.wasm', 'https://mp/x', 'deadbeef', '0.1.0', '1', '2026-07-17')`); err != nil {
		t.Fatalf("seed plugin_catalog: %v", err)
	}
	if _, err := d.DB.Exec(`INSERT INTO plugins (id, name, version, entrypoint) VALUES ('com.t.ux', 'Ux Plugin', '1.0.0', 'p.wasm')`); err != nil {
		t.Fatalf("seed plugins: %v", err)
	}
	if _, err := d.DB.Exec(`INSERT INTO plugin_settings (id, plugin_id, key, value_json, scope, scope_id) VALUES
		('ux-1', 'com.t.ux', 'apiKey', '"a"', 'global', NULL)`); err != nil {
		t.Fatalf("seed first global row: %v", err)
	}

	_, err = d.DB.Exec(`INSERT INTO plugin_settings (id, plugin_id, key, value_json, scope, scope_id) VALUES
		('ux-2', 'com.t.ux', 'apiKey', '"b"', 'global', NULL)`)
	if err == nil {
		t.Fatal("expected a second global row for the same (plugin_id, key) to be rejected")
	}
	if !strings.Contains(err.Error(), "UNIQUE constraint failed: plugin_settings.plugin_id, plugin_settings.key") {
		t.Fatalf("insert failed for an unexpected reason, want the ux_plugin_settings_global constraint: %v", err)
	}

	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM plugin_settings WHERE plugin_id = 'com.t.ux'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("row count = %d, want 1 (the rejected insert must not have landed)", n)
	}
}
