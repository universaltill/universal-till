// Command seed_api_importer installs a test API-source import plugin
// (ADR-0161) into a throwaway e2e till, for import-from-system-4431.spec.ts:
// the real compiled WASM guest internal/plugins/testdata/importrows_guest,
// which answers "import.requested.ask" by submitting the rows held in its
// own "rows" setting. Like seed_faq it writes the DB rows directly and puts
// the module where the WASM runtime loads it from
// (<data dir>/plugins/<id>/<version>/plugin.wasm); the till loads it at
// boot. Run from the repo root (worker-till.ts's extraSeeds) with
// UT_DATA_DIR set, before the till starts. Generic names only.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/universaltill/universal-till/internal/db"
)

const (
	pluginID      = "com.example.e2e-api-importer"
	pluginName    = "Sample Till Connector"
	pluginVersion = "1.0.0"
)

// rows is what the guest submits: two good items and one with no name, so
// the preview shows new rows and an issue.
const rows = `[{"external_id":"e2e-1","name":"E2E Flat white","price_minor":320,"category_path":["E2E Drinks"]},` +
	`{"external_id":"e2e-2","name":"E2E Scone","price_minor":250},` +
	`{"external_id":"e2e-bad","price_minor":1}]`

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(2)
}

func main() {
	dataDir := os.Getenv("UT_DATA_DIR")
	if dataDir == "" {
		fatalf("UT_DATA_DIR must be set")
	}
	modDir := filepath.Join(dataDir, "plugins", pluginID, pluginVersion)
	if err := os.MkdirAll(modDir, 0o755); err != nil {
		fatalf("mkdir %s: %v", modDir, err)
	}
	build := exec.Command("go", "build", "-o", filepath.Join(modDir, "plugin.wasm"), "./internal/plugins/testdata/importrows_guest")
	build.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if out, err := build.CombinedOutput(); err != nil {
		fatalf("build importrows guest: %v\n%s", err, out)
	}

	conn, err := db.Open(filepath.Join(dataDir, "unitill-pos.db"))
	if err != nil {
		fatalf("open db: %v", err)
	}
	defer conn.Close()
	tx, err := conn.Begin()
	if err != nil {
		fatalf("begin: %v", err)
	}
	execSQL := func(q string, args ...any) {
		if _, err := tx.Exec(q, args...); err != nil {
			_ = tx.Rollback()
			fatalf("exec failed: %v -- %s", err, q)
		}
	}
	execSQL(`INSERT OR IGNORE INTO plugin_catalog
		(id, version, name, description, author, website, runtime, entrypoint, package_url, sha256, min_pos_version, api_version, published_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?, datetime('now'))`,
		pluginID, pluginVersion, pluginName, "", "", "", "wasm", "./plugin.wasm", "local-e2e-fixture",
		strings.Repeat("0", 64), "1.0.0", "1.0")
	execSQL(`INSERT OR IGNORE INTO plugins (id, name, version, entrypoint, runtime, is_active) VALUES (?,?,?,?,?,1)`,
		pluginID, pluginName, pluginVersion, "./plugin.wasm", "wasm")
	execSQL(`INSERT OR IGNORE INTO plugin_entries (id, plugin_id, type, key, label, is_active, sort_order, config_json)
		VALUES (?,?,?,?,?,1,0,?)`,
		"e2e-api-importer-entry", pluginID, "import", "api", "From another till system", `{"entities":["items"],"source":"api"}`)
	for _, perm := range []string{"events:receive", "items:write"} {
		execSQL(`INSERT OR IGNORE INTO plugin_permissions (id, plugin_id, permission, granted) VALUES (?,?,?,1)`,
			"e2e-api-importer-"+perm, pluginID, perm)
	}
	execSQL(`INSERT OR IGNORE INTO plugin_hooks (id, plugin_id, event, action, is_active) VALUES (?,?,?,?,1)`,
		"e2e-api-importer-hook", pluginID, "import.requested.ask", "import")
	// The guest's row source, plus the two credential settings an API
	// importer declares: one set, one not — the page shows set / not set.
	for key, value := range map[string]string{"rows": rows, "api_key": "e2e-not-a-real-key", "api_secret": ""} {
		valueJSON, err := json.Marshal(value)
		if err != nil {
			fatalf("encode %s: %v", key, err)
		}
		execSQL(`INSERT OR IGNORE INTO plugin_settings (id, plugin_id, key, value_json, scope) VALUES (?,?,?,?,?)`,
			"e2e-api-importer-"+key, pluginID, key, string(valueJSON), "global")
	}
	if err := tx.Commit(); err != nil {
		fatalf("commit: %v", err)
	}
	fmt.Println("Seeded API importer plugin for e2e")
}
