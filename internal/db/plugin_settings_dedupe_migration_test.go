package db

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestMigration052DedupesGlobalPluginSettings simulates a till that already
// has duplicate scope='global' plugin_settings rows from before ut-docs#785's
// race fix, and confirms migration 052 repairs it on upgrade: the
// most-recently-updated row per (plugin_id, key) survives, and
// non-duplicated / non-global rows are left untouched. See
// internal/db/migrations/052_dedupe_plugin_settings_global.sql.
func TestMigration052DedupesGlobalPluginSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m052-upgrade.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := d.DB.Exec(`INSERT INTO plugin_catalog
		(id, version, name, runtime, entrypoint, package_url, sha256, min_pos_version, api_version, published_at)
		VALUES ('com.t.dupe', '1.0.0', 'Dupe Plugin', 'wasm', 'p.wasm', 'https://mp/x', 'deadbeef', '0.1.0', '1', '2026-07-17')`); err != nil {
		t.Fatalf("seed plugin_catalog: %v", err)
	}
	if _, err := d.DB.Exec(`INSERT INTO plugins (id, name, version, entrypoint) VALUES ('com.t.dupe', 'Dupe Plugin', '1.0.0', 'p.wasm')`); err != nil {
		t.Fatalf("seed plugins: %v", err)
	}

	// ux_plugin_settings_global (migration 053, ut-docs#787) already exists
	// on this freshly-opened DB and would reject the very duplicate this
	// test needs to seed -- drop it first so the seed below can recreate
	// the pre-repair state; the rewind-and-reopen below (which replays 052
	// then 053 in order) recreates it once the duplicate is cleaned up.
	if _, err := d.DB.Exec(`DROP INDEX IF EXISTS ux_plugin_settings_global`); err != nil {
		t.Fatalf("drop unique index for reseed: %v", err)
	}

	// Simulate the pre-785 race: two 'global' rows for the same key, the
	// unique index didn't catch it because scope_id is NULL on both. The
	// newer one (later updated_at) holds the value an operator actually
	// configured; the older one is the stale leftover.
	if _, err := d.DB.Exec(`INSERT INTO plugin_settings (id, plugin_id, key, value_json, scope, scope_id, updated_at) VALUES
		('dup-old', 'com.t.dupe', 'apiKey', '"stale"', 'global', NULL, '2026-08-10T00:00:00Z'),
		('dup-new', 'com.t.dupe', 'apiKey', '"configured"', 'global', NULL, '2026-08-12T00:00:00Z')`); err != nil {
		t.Fatalf("seed duplicate global rows: %v", err)
	}
	// A non-duplicated global row must survive untouched.
	if _, err := d.DB.Exec(`INSERT INTO plugin_settings (id, plugin_id, key, value_json, scope, scope_id, updated_at) VALUES
		('single', 'com.t.dupe', 'otherKey', '"solo"', 'global', NULL, '2026-08-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed non-duplicate global row: %v", err)
	}
	// A register-scoped pair sharing a key with a NULL scope_id must be
	// left alone -- 052 is deliberately scoped to scope='global' only (see
	// the migration's own header comment).
	if _, err := d.DB.Exec(`INSERT INTO plugin_settings (id, plugin_id, key, value_json, scope, scope_id, updated_at) VALUES
		('reg-a', 'com.t.dupe', 'readerIp', '"1.2.3.4"', 'register', NULL, '2026-08-05T00:00:00Z'),
		('reg-b', 'com.t.dupe', 'readerIp', '"5.6.7.8"', 'register', NULL, '2026-08-06T00:00:00Z')`); err != nil {
		t.Fatalf("seed register-scoped rows: %v", err)
	}

	// Rewind the ledger so 052 replays on reopen. 052 itself is pure DML (no
	// ALTER TABLE), but 054 (which replays too) creates the floor-plan
	// `tables` table and held_sales.table_id — undo that DDL first.
	rewindTables054(t, d)
	if _, err := d.DB.Exec(`DELETE FROM schema_migrations WHERE version >= 52`); err != nil {
		t.Fatalf("rewind schema_migrations: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	d, err = Open(path) // replays 052 against the simulated pre-repair till
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	rows, err := d.DB.Query(`SELECT id, value_json FROM plugin_settings WHERE plugin_id = 'com.t.dupe' AND scope = 'global' AND key = 'apiKey'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []struct{ id, value string }
	for rows.Next() {
		var r struct{ id, value string }
		if err := rows.Scan(&r.id, &r.value); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("global apiKey rows after 052 = %d, want exactly 1 (%v)", len(got), got)
	}
	if got[0].id != "dup-new" || got[0].value != `"configured"` {
		t.Errorf("surviving row = %+v, want the newer row (dup-new, \"configured\")", got[0])
	}

	// The non-duplicated global row survives untouched.
	var soloValue string
	if err := d.DB.QueryRow(`SELECT value_json FROM plugin_settings WHERE id = 'single'`).Scan(&soloValue); err != nil {
		t.Fatalf("non-duplicate global row was affected: %v", err)
	}
	if soloValue != `"solo"` {
		t.Errorf("non-duplicate global row value = %q, want %q", soloValue, `"solo"`)
	}

	// Both register-scoped rows survive -- 052 doesn't touch scope != 'global'.
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM plugin_settings WHERE scope = 'register' AND key = 'readerIp'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("register-scoped readerIp rows = %d, want 2 (052 must not touch non-global scopes)", n)
	}
}

// TestMigration052IsIdempotentOnCleanData confirms 052 is a genuine no-op
// the SECOND time it runs against a till with no duplicates -- the common
// case, since #785 already stops new ones from forming, and the case every
// till hits on its next-after-052 upgrade once it's already clean. Actually
// re-applies 052 (not just checks the seeded state) so this test would fail
// if a future edit made the DELETE non-idempotent (e.g. a tiebreak that
// isn't stable, or a WHERE clause that widens on a second pass).
func TestMigration052IsIdempotentOnCleanData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m052-clean.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := d.DB.Exec(`INSERT INTO plugin_catalog
		(id, version, name, runtime, entrypoint, package_url, sha256, min_pos_version, api_version, published_at)
		VALUES ('com.t.clean', '1.0.0', 'Clean Plugin', 'wasm', 'p.wasm', 'https://mp/x', 'deadbeef', '0.1.0', '1', '2026-07-17')`); err != nil {
		t.Fatalf("seed plugin_catalog: %v", err)
	}
	if _, err := d.DB.Exec(`INSERT INTO plugins (id, name, version, entrypoint) VALUES ('com.t.clean', 'Clean Plugin', '1.0.0', 'p.wasm')`); err != nil {
		t.Fatalf("seed plugins: %v", err)
	}
	if _, err := d.DB.Exec(`INSERT INTO plugin_settings (id, plugin_id, key, value_json, scope, scope_id, updated_at) VALUES
		('c1', 'com.t.clean', 'apiKey', '"v"', 'global', NULL, '2026-08-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed single global row: %v", err)
	}

	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM plugin_settings WHERE plugin_id = 'com.t.clean'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("seeded row count = %d, want 1", n)
	}

	// Rewind and re-apply 052 against data it already left clean (054
	// replays too, so its DDL is undone first).
	rewindTables054(t, d)
	if _, err := d.DB.Exec(`DELETE FROM schema_migrations WHERE version >= 52`); err != nil {
		t.Fatalf("rewind schema_migrations: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	d, err = Open(path) // re-applies 052 a second time
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	var id, value string
	if err := d.DB.QueryRow(`SELECT id, value_json FROM plugin_settings WHERE plugin_id = 'com.t.clean'`).Scan(&id, &value); err != nil {
		t.Fatalf("row missing after second application of 052: %v", err)
	}
	if id != "c1" || value != `"v"` {
		t.Errorf("surviving row after re-applying 052 = (%s, %s), want unchanged (c1, \"v\")", id, value)
	}
}

// TestUxPluginSettingsGlobalRejectsDuplicateRow confirms the schema-level
// backstop itself (ux_plugin_settings_global, migration 053) still rejects
// a genuine duplicate global row at the DB level — moved here (ut-docs#807)
// from a sync-apply test now that the apply path (applyPluginSettings,
// internal/data/sync_admin_repo.go) defensively dedupes before it ever
// reaches this index, so a bundle carrying a stale-primary duplicate no
// longer exercises this constraint at all. The index itself must still
// exist and still work for any other writer that isn't going through the
// sync-apply path's dedupe.
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
