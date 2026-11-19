// Command seed_card_readers installs the readers_guest test payment plugin
// (declares payment.e2ecard.readers, ut-docs#6566) and a card reader registry
// of generic names into a throwaway e2e/docs till, so the "Which reader?"
// picker and the Card readers page have real rows to show. Like seed_faq it
// writes the DB rows directly and puts the module where the WASM runtime
// loads it from (<data dir>/plugins/<id>/<version>/plugin.wasm); the till
// loads it at boot. Run from the repo root with UT_DATA_DIR set, before the
// till starts. Idempotent (INSERT OR IGNORE). Reader ids containing offline /
// busy / setup / slow drive the matching picker state (see readers_guest).
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
	pluginID      = "com.example.e2e-card-readers"
	pluginName    = "Sample Card Terminals"
	pluginVersion = "1.0.0"
	methodKey     = "e2ecard"
)

var readers = []struct{ id, name, ref string }{
	{"e2e-reader-1", "Front counter", "tmr_front"},
	{"e2e-reader-2", "Back office", "tmr_back"},
	{"e2e-reader-3", "Bar", "tmr_offline_bar"},
}

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
	build := exec.Command("go", "build", "-o", filepath.Join(modDir, "plugin.wasm"), "./internal/plugins/testdata/readers_guest")
	build.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if out, err := build.CombinedOutput(); err != nil {
		fatalf("build readers guest: %v\n%s", err, out)
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
	execSQL(`INSERT OR IGNORE INTO plugin_entries (id, plugin_id, type, key, label, trigger_event, is_active, sort_order)
		VALUES (?,?,?,?,?,?,1,0)`,
		"e2e-card-readers-entry", pluginID, "payment", methodKey, "Card terminal", "payment."+methodKey+".requested")
	execSQL(`INSERT OR IGNORE INTO plugin_permissions (id, plugin_id, permission, granted) VALUES (?,?,?,1)`,
		"e2e-card-readers-events", pluginID, "events:receive")
	for _, ev := range []string{"authorize", "refund", "readers"} {
		execSQL(`INSERT OR IGNORE INTO plugin_hooks (id, plugin_id, event, action, is_active) VALUES (?,?,?,?,1)`,
			"e2e-card-readers-hook-"+ev, pluginID, "payment."+methodKey+"."+ev, ev)
	}
	for i, r := range readers {
		execSQL(`INSERT OR IGNORE INTO card_readers (id, name, method_key, plugin_id, reader_ref, kind, source, sort_order)
			VALUES (?,?,?,?,?,?,?,?)`, r.id, r.name, methodKey, pluginID, r.ref, "cloud", "manual", i)
	}
	if err := tx.Commit(); err != nil {
		fatalf("commit: %v", err)
	}
	fmt.Println("Seeded card terminal plugin and readers for e2e")
}
