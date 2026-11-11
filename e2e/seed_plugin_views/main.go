// Command seed_plugin_views installs a test view plugin (ADR-0121 plugin
// views, ut-docs#4378) into a throwaway e2e till, for
// plugin-views-4378.spec.ts: the real compiled WASM guest
// internal/plugins/testdata/views_guest, which answers ui.view.ask /
// ui.action.ask with a chat, a wizard (+ qr) and a long-job document. Like
// seed_identify_plugin it writes the DB rows directly and puts the module
// where the WASM runtime loads it from
// (<data dir>/plugins/<id>/<version>/plugin.wasm). It writes no
// manifest.json, so the job deadline falls back to the default. The plugin
// declares no ai.agent, so ADR-0153's event data-class gate does not apply.
// Its own locale bundle (e2e/seed_plugin_views/locales) is a test fixture,
// not core i18n. Run from the repo root (worker-till.ts's extraSeeds) with
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
	pluginID      = "com.example.e2e-views"
	pluginName    = "Sample View Plugin"
	pluginVersion = "1.0.0"
	slug          = "e2e-views"
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
	build := exec.Command("go", "build", "-o", filepath.Join(modDir, "plugin.wasm"), "./internal/plugins/testdata/views_guest")
	build.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if out, err := build.CombinedOutput(); err != nil {
		fatalf("build views guest: %v\n%s", err, out)
	}

	// The plugin's own locale bundle, read relative to the repo root.
	src := filepath.Join("e2e", "seed_plugin_views", "locales")
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
	for _, perm := range []string{"ui:page", "ui:slot:reports.panels", "events:receive"} {
		execSQL(`INSERT OR IGNORE INTO plugin_permissions (id, plugin_id, permission, granted) VALUES (?,?,?,1)`,
			"e2e-views-"+perm, pluginID, perm)
	}
	for i, h := range [][2]string{
		{"ui.view.ask", "view"},
		{"ui.action.ask", "view"},
		{pluginID + ".chat", "chat"},
		{pluginID + ".work", "work"},
	} {
		execSQL(`INSERT OR IGNORE INTO plugin_hooks (id, plugin_id, event, action, is_active) VALUES (?,?,?,?,1)`,
			fmt.Sprintf("e2e-views-hook-%d", i), pluginID, h[0], h[1])
	}
	for _, k := range []string{"chat", "wizard", "job"} {
		execSQL(`INSERT OR IGNORE INTO plugin_entries (id, plugin_id, type, key, label, menu_group, route, config_json)
			VALUES (?,?,?,?,?,?,?,?)`,
			slug+"-"+k, pluginID, "page", k, "e2eviews."+k+".title", "main",
			"/plugin/"+slug+"/"+k, `{"view":"e2eviews.`+k+`"}`)
	}
	// Two content slot panels on Reports for plugin-slot-paging-4114.spec.ts:
	// paged_a pages over three pages, paged_b is one static table.
	for _, k := range []string{"paged_a", "paged_b"} {
		execSQL(`INSERT OR IGNORE INTO plugin_entries (id, plugin_id, type, key, label, menu_group, route, config_json)
			VALUES (?,?,?,?,?,?,?,?)`,
			slug+"-"+k, pluginID, "page", k, "e2eviews."+k+".title", "",
			"/plugin/"+slug+"/"+k, `{"view":"e2eviews.`+k+`","content_slot":"reports.panels"}`)
	}
	if err := tx.Commit(); err != nil {
		fatalf("commit: %v", err)
	}
	fmt.Println("Seeded plugin views for e2e")
}
