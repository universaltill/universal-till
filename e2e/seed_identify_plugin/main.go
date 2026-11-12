// Command seed_identify_plugin installs a test camera-identify plugin
// (ADR-0121 §7 catalog.identify seam, ut-docs#4102) into a throwaway e2e
// till, for plugin-identify-camera-errors-4102.spec.ts: the real compiled
// WASM guest internal/plugins/testdata/identify_guest, which answers every
// identify job with one add_to_basket suggestion for the SKU in its "sku"
// setting. Like seed_api_importer it writes the DB rows directly and puts
// the module where the WASM runtime loads it from
// (<data dir>/plugins/<id>/<version>/plugin.wasm); the till loads it at
// boot. The plugin declares no ai.agent, so ADR-0153's event data-class
// gate does not apply and events:receive is all it needs. Run from the
// repo root (worker-till.ts's extraSeeds) with UT_DATA_DIR set, before the
// till starts. Generic names only.
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
	pluginID      = "com.example.e2e-identify"
	pluginName    = "Sample Identify Plugin"
	pluginVersion = "1.0.0"
	// Coca-Cola 330ml, internal/data/seeddata/demo_catalogue.sql.
	sku = "2000010000012"
	// label differs from the guest's built-in default, so the spec proves
	// the guest really read its settings.
	label = "Sample identified drink"
)

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
	build := exec.Command("go", "build", "-o", filepath.Join(modDir, "plugin.wasm"), "./internal/plugins/testdata/identify_guest")
	build.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if out, err := build.CombinedOutput(); err != nil {
		fatalf("build identify guest: %v\n%s", err, out)
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
	for _, perm := range []string{"events:receive"} {
		execSQL(`INSERT OR IGNORE INTO plugin_permissions (id, plugin_id, permission, granted) VALUES (?,?,?,1)`,
			"e2e-identify-"+perm, pluginID, perm)
	}
	execSQL(`INSERT OR IGNORE INTO plugin_hooks (id, plugin_id, event, action, is_active) VALUES (?,?,?,?,1)`,
		"e2e-identify-hook", pluginID, "catalog.identify", "identify")
	for key, value := range map[string]string{"sku": sku, "label": label} {
		valueJSON, err := json.Marshal(value)
		if err != nil {
			fatalf("encode %s: %v", key, err)
		}
		execSQL(`INSERT OR IGNORE INTO plugin_settings (id, plugin_id, key, value_json, scope) VALUES (?,?,?,?,?)`,
			"e2e-identify-"+key, pluginID, key, string(valueJSON), "global")
	}
	if err := tx.Commit(); err != nil {
		fatalf("commit: %v", err)
	}
	fmt.Println("Seeded identify plugin for e2e")
}
