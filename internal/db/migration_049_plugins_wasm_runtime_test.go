package db

import "testing"

// pluginsWasmRuntimeMigrationVersion is 049_plugins_wasm_entrypoint_runtime.sql
// (ut-docs#2891). If a concurrent lane claims 049 first, the file and this
// constant move together.
const pluginsWasmRuntimeMigrationVersion = 49

// TestMigration049_WasmEntrypointRowsLeaveTheGoRuntime: ut-docs#2891 made a
// manifest without "runtime" default to the wasm sandbox, but plugins
// installed before it were persisted with the column default 'go' even when
// their entrypoint is a .wasm module. Those rows are corrected to 'wasm';
// a genuine go plugin (non-.wasm entrypoint) and rows already on another
// runtime are untouched, and a replay is a no-op.
func TestMigration049_WasmEntrypointRowsLeaveTheGoRuntime(t *testing.T) {
	d, err := Open(t.TempDir() + "/plugins-wasm-runtime.db")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	var applied int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, pluginsWasmRuntimeMigrationVersion).Scan(&applied); err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	if applied != 1 {
		t.Fatalf("migration %d not recorded as applied on a fresh DB — has it been renumbered?", pluginsWasmRuntimeMigrationVersion)
	}

	rows := []struct{ id, entrypoint, runtime, want string }{
		{"com.t.wasm", "./plugin.wasm", "go", "wasm"},
		{"com.t.wasmbare", "plugin.wasm", "go", "wasm"},
		{"com.t.wasmupper", "./PLUGIN.WASM", "go", "wasm"},
		{"com.t.gobin", "./plugin", "go", "go"},
		{"com.t.gowasmdir", "./wasm/run", "go", "go"},
		{"com.t.already", "./plugin.wasm", "wasm", "wasm"},
		{"com.t.none", "./x.wasm", "none", "none"},
	}
	for _, r := range rows {
		if _, err := d.DB.Exec(`INSERT INTO plugin_catalog
			(id, version, name, runtime, entrypoint, package_url, sha256, min_pos_version, api_version, published_at)
			VALUES (?, '1.0.0', ?, ?, ?, 'https://mp/x', 'deadbeef', '0.1.0', '1', '2026-09-26')`, r.id, r.id, r.runtime, r.entrypoint); err != nil {
			t.Fatalf("seed catalog %s: %v", r.id, err)
		}
		if _, err := d.DB.Exec(`INSERT INTO plugins (id, name, version, entrypoint, runtime) VALUES (?, ?, '1.0.0', ?, ?)`, r.id, r.id, r.entrypoint, r.runtime); err != nil {
			t.Fatalf("seed %s: %v", r.id, err)
		}
	}
	m := loadMigrationVersion(t, pluginsWasmRuntimeMigrationVersion)
	for pass := 0; pass < 2; pass++ { // apply, then replay
		if _, err := d.DB.Exec(`DELETE FROM schema_migrations WHERE version = ?`, pluginsWasmRuntimeMigrationVersion); err != nil {
			t.Fatal(err)
		}
		if err := d.applyMigration(m); err != nil {
			t.Fatalf("pass %d: applying migration %d: %v", pass, pluginsWasmRuntimeMigrationVersion, err)
		}
		for _, r := range rows {
			var got string
			if err := d.DB.QueryRow(`SELECT runtime FROM plugins WHERE id = ?`, r.id).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != r.want {
				t.Errorf("pass %d: %s (entrypoint %s, was %s) runtime = %q, want %q", pass, r.id, r.entrypoint, r.runtime, got, r.want)
			}
		}
	}
}
