// Command seed_file_importer installs a test file-rows import plugin
// (ADR-0164 §2, "source": "file") into a throwaway e2e till, for
// import-file-plugin-4526.spec.ts: the real compiled WASM guest
// internal/plugins/testdata/filerows_guest, which reads the uploaded file
// (header fixture_ref,fixture_title,fixture_price_minor) and submits one
// item row per line, in the import sandbox. Like seed_faq it writes the DB rows directly and puts
// the module where the WASM runtime loads it from
// (<data dir>/plugins/<id>/<version>/plugin.wasm); the till loads it at
// boot. Run from the repo root (worker-till.ts's extraSeeds) with
// UT_DATA_DIR set, before the till starts. Generic names only.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/universaltill/universal-till/internal/db"
)

const (
	pluginID      = "com.example.e2e-file-importer"
	pluginName    = "Sample Export Reader"
	pluginVersion = "1.0.0"
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
	build := exec.Command("go", "build", "-o", filepath.Join(modDir, "plugin.wasm"), "./internal/plugins/testdata/filerows_guest")
	build.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if out, err := build.CombinedOutput(); err != nil {
		fatalf("build filerows guest: %v\n%s", err, out)
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
		"e2e-file-importer-entry", pluginID, "import", "export", "Back office export",
		`{"entities":["items"],"source":"file","file_formats":[".csv"],"file_headers":["fixture_ref","fixture_title"]}`)
	for _, perm := range []string{"events:receive", "items:write"} {
		execSQL(`INSERT OR IGNORE INTO plugin_permissions (id, plugin_id, permission, granted) VALUES (?,?,?,1)`,
			"e2e-file-importer-"+perm, pluginID, perm)
	}
	execSQL(`INSERT OR IGNORE INTO plugin_hooks (id, plugin_id, event, action, is_active) VALUES (?,?,?,?,1)`,
		"e2e-file-importer-hook", pluginID, "import.requested.ask", "import")
	if err := tx.Commit(); err != nil {
		fatalf("commit: %v", err)
	}
	fmt.Println("Seeded file importer plugin for e2e")
}
