// Command seed_agent_plugin installs a test AI-agent plugin (ut-docs#5466)
// into a throwaway e2e till, for plugin-confirm-5466.spec.ts: the compiled
// WASM guest internal/plugins/testdata/agent_guest proposes a catalogue item
// on ui.view.ask and returns core's `confirm` component for it.
//
// Why a separate plugin from com.example.e2e-views (seed_plugin_views):
// giving that one ai.agent
// would put its chat/job events under ADR-0153's data-class gate.
//
// Why the till needs auth on: core stamps an agent operator only from an
// auth session (pages.withAgentOperator); with UT_AUTH=off every
// action_propose is refused (-2, ut-docs#4168). worker-till.ts's `auth`
// option boots such a till. No PIN hash is seeded: first boot goes through
// POST /api/auth/setup there.
//
// Unlike seed_plugin_views this also writes <version>/manifest.json, because
// the propose path reads the installed manifest (InstalledManifest) for
// ai.agent. It is unsigned: the signature is only checked at install, never
// at WASM load (same test-support precedent as the other seeds). Own locale
// bundle (e2e/seed_agent_plugin/locales) is a fixture, not core i18n. Run
// from the repo root with UT_DATA_DIR set, before the till starts.
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
	pluginID      = "com.example.e2e-agent"
	pluginName    = "Sample Agent Plugin"
	pluginVersion = "1.0.0"
	slug          = "e2e-agent"
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
	locDir := filepath.Join(modDir, "locales")
	if err := os.MkdirAll(locDir, 0o755); err != nil {
		fatalf("mkdir %s: %v", locDir, err)
	}
	build := exec.Command("go", "build", "-o", filepath.Join(modDir, "plugin.wasm"), "./internal/plugins/testdata/agent_guest")
	build.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if out, err := build.CombinedOutput(); err != nil {
		fatalf("build agent guest: %v\n%s", err, out)
	}

	manifest := `{"id":"` + pluginID + `","name":"` + pluginName + `","version":"` + pluginVersion + `",` +
		`"entrypoint":"./plugin.wasm","runtime":"wasm",` +
		`"permissions":["ai.agent","ai.tools.write:catalog","ui:page","events:receive"]}`
	if err := os.WriteFile(filepath.Join(modDir, "manifest.json"), []byte(manifest), 0o644); err != nil {
		fatalf("write manifest: %v", err)
	}

	// The plugin's own locale bundle, read relative to the repo root.
	src := filepath.Join("e2e", "seed_agent_plugin", "locales")
	files, err := os.ReadDir(src)
	if err != nil {
		fatalf("read %s: %v", src, err)
	}
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(src, f.Name()))
		if err != nil {
			fatalf("read locale %s: %v", f.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(locDir, f.Name()), b, 0o644); err != nil {
			fatalf("write locale %s: %v", f.Name(), err)
		}
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
	for _, perm := range []string{"ai.agent", "ai.tools.write:catalog", "ui:page", "events:receive"} {
		execSQL(`INSERT OR IGNORE INTO plugin_permissions (id, plugin_id, permission, granted) VALUES (?,?,?,1)`,
			"e2e-agent-"+perm, pluginID, perm)
	}
	execSQL(`INSERT OR IGNORE INTO plugin_hooks (id, plugin_id, event, action, is_active) VALUES (?,?,?,?,1)`,
		"e2e-agent-hook-0", pluginID, "ui.view.ask", "view")
	execSQL(`INSERT OR IGNORE INTO plugin_entries (id, plugin_id, type, key, label, menu_group, route, config_json)
		VALUES (?,?,?,?,?,?,?,?)`,
		slug+"-confirm", pluginID, "page", "confirm", "e2eagent.confirm.title", "main",
		"/plugin/"+slug+"/confirm", `{"view":"e2eagent.confirm"}`)
	if err := tx.Commit(); err != nil {
		fatalf("commit: %v", err)
	}
	fmt.Println("Seeded agent plugin for e2e")
}
