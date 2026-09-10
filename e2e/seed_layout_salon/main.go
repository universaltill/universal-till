// Command seed_layout_salon installs the REAL plugins/layout-salon plugin
// (ADR-0088, ut-docs#1904) into a throwaway e2e till, so
// layout-plugin-menu-1904.spec.ts drives the actual amendment path — a
// `layout` entry's config document parsed by internal/uislot, resolved on
// the render path, and undone through the Settings restore surface —
// against a genuinely installed plugin rather than a mocked route.
//
// The manifest and locale files are READ FROM plugins/layout-salon itself,
// not duplicated as fixtures: if the shipped plugin's amendments change,
// this seeder installs the changed ones and the spec's assertions move with
// it, instead of quietly testing a stale copy.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/universaltill/universal-till/internal/db"
)

type manifest struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
	Entries []struct {
		Type   string         `json:"type"`
		Key    string         `json:"key"`
		Label  string         `json:"label"`
		Config map[string]any `json:"config"`
	} `json:"entries"`
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

	// run-till.sh cd's to the repo root before invoking this.
	src := filepath.Join("plugins", "layout-salon")
	raw, err := os.ReadFile(filepath.Join(src, "plugin.json"))
	if err != nil {
		fatalf("read plugins/layout-salon/plugin.json: %v", err)
	}
	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		fatalf("parse manifest: %v", err)
	}
	if len(m.Entries) == 0 {
		fatalf("plugins/layout-salon declares no entries — nothing to seed")
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
	exec := func(q string, args ...any) {
		if _, err := tx.Exec(q, args...); err != nil {
			_ = tx.Rollback()
			fatalf("exec failed: %v -- %s", err, q)
		}
	}

	exec(`INSERT OR IGNORE INTO plugin_catalog
		(id, version, name, description, author, website, runtime, entrypoint, package_url, sha256, min_pos_version, api_version, published_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?, datetime('now'))`,
		m.ID, m.Version, m.Name, "", "", "", "none", "", "local-e2e-fixture",
		strings.Repeat("0", 64), "1.0.0", "1.0")

	exec(`INSERT OR IGNORE INTO plugins (id, name, version, entrypoint, runtime)
		VALUES (?,?,?,?,?)`, m.ID, m.Name, m.Version, "", "none")

	// ut-docs#2001: Init() now reconciles builtinlayouts.Sync against
	// shop_type on every boot, and Sync is keyed on shop_type alone — an
	// installed-but-not-"service" salon plugin is exactly what it's
	// designed to remove (see builtinlayouts.Sync's own doc comment: a
	// plugin left installed after shop_type moves away must not survive a
	// reload). This seeder installs the plugin directly, bypassing the
	// shop_type=service flow that's the ONLY real path to it existing at
	// all — without this, the very first boot after seeding would
	// immediately uninstall what was just seeded. "shop.type" is
	// common.KeyShopType's value; not importing internal/pages/common here
	// to keep this seeder's dependency footprint matching seed_demo's.
	exec(`INSERT OR IGNORE INTO settings (key, value) VALUES ('shop.type', 'service')`)

	for i, e := range m.Entries {
		cfg, err := json.Marshal(e.Config)
		if err != nil {
			_ = tx.Rollback()
			fatalf("marshal entry %q config: %v", e.Key, err)
		}
		exec(`INSERT OR IGNORE INTO plugin_entries (id, plugin_id, type, key, label, config_json)
			VALUES (?,?,?,?,?,?)`,
			fmt.Sprintf("e2e-layout-salon-%d", i), m.ID, e.Type, e.Key, e.Label, string(cfg))
	}

	if err := tx.Commit(); err != nil {
		fatalf("commit: %v", err)
	}

	// Real locale files, so the re-labelled tile renders its translated
	// string through syncLocales exactly as a real install would, rather
	// than falling back to the core label and making the spec pass for the
	// wrong reason.
	dst := filepath.Join(dataDir, "plugins", m.ID, m.Version, "locales")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		fatalf("mkdir %s: %v", dst, err)
	}
	entries, err := os.ReadDir(filepath.Join(src, "locales"))
	if err != nil {
		fatalf("read plugin locales: %v", err)
	}
	for _, f := range entries {
		if f.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(src, "locales", f.Name()))
		if err != nil {
			fatalf("read locale %s: %v", f.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(dst, f.Name()), b, 0o644); err != nil {
			fatalf("write locale %s: %v", f.Name(), err)
		}
	}

	fmt.Printf("Seeded layout-salon plugin (%s %s, %d entries) for e2e\n", m.ID, m.Version, len(m.Entries))
}
