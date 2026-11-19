// Command seed_settings_plugin installs a test plugin with an asked settings
// dropdown (ADR-0121 S5, ut-docs#4160) into a throwaway e2e till, for
// plugin-settings-options-4160.spec.ts: the real compiled WASM guest
// internal/plugins/testdata/settings_guest, which answers
// ui.settings.options.ask for its "model" setting from the unsaved
// "provider". Like seed_plugin_views it writes the DB rows directly and puts
// the module where the WASM runtime loads it from
// (<data dir>/plugins/<id>/<version>/plugin.wasm); unlike it, it also writes
// manifest.json, because the settings page reads its form from there. The
// stored model ("retired-model") is deliberately in no answer. Run from the
// repo root (worker-till.ts's extraSeeds) with UT_DATA_DIR set, before the
// till starts. Generic names only.
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
	pluginID      = "com.example.e2e-settings"
	pluginName    = "Sample Settings Plugin"
	pluginVersion = "1.0.0"
)

const manifest = `{
  "id": "com.example.e2e-settings",
  "name": "Sample Settings Plugin",
  "version": "1.0.0",
  "runtime": "wasm",
  "entrypoint": "./plugin.wasm",
  "permissions": ["events:receive"],
  "hooks": [{"event": "ui.settings.options.ask", "action": "settings"}],
  "settings": [
    {"key": "provider", "type": "choice", "label": "plugins.settings.title",
     "options": [
       {"value": "a", "label": "plugins.settings.permissions.granted"},
       {"value": "b", "label": "plugins.settings.permissions.not_granted"},
       {"value": "c", "label": "plugins.settings.save"},
       {"value": "d", "label": "plugins.settings.none"}]},
    {"key": "model", "type": "choice", "label": "plugins.settings.title", "options_ask": true, "depends_on": ["provider"]}
  ]
}
`

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
	build := exec.Command("go", "build", "-o", filepath.Join(modDir, "plugin.wasm"), "./internal/plugins/testdata/settings_guest")
	build.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if out, err := build.CombinedOutput(); err != nil {
		fatalf("build settings guest: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(modDir, "manifest.json"), []byte(manifest), 0o644); err != nil {
		fatalf("write manifest: %v", err)
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
	execSQL(`INSERT OR IGNORE INTO plugin_permissions (id, plugin_id, permission, granted) VALUES (?,?,?,1)`,
		"e2e-settings-events-receive", pluginID, "events:receive")
	execSQL(`INSERT OR IGNORE INTO plugin_hooks (id, plugin_id, event, action, is_active) VALUES (?,?,?,?,1)`,
		"e2e-settings-hook-0", pluginID, "ui.settings.options.ask", "settings")
	for k, v := range map[string]string{"provider": `"a"`, "model": `"retired-model"`} {
		execSQL(`INSERT OR IGNORE INTO plugin_settings (id, plugin_id, key, value_json, scope, updated_at) VALUES (?,?,?,?,'global', datetime('now'))`,
			"e2e-settings-"+k, pluginID, k, v)
	}
	if err := tx.Commit(); err != nil {
		fatalf("commit: %v", err)
	}
	fmt.Println("Seeded settings plugin for e2e")
}
