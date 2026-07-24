// Command seed_faq installs the real FAQ plugin (page entry + content
// bundles) into a throwaway e2e till so faq.spec.ts can exercise the actual
// /plugin/faq render path — locale fallback, RTL, and the client-side
// search JS — against a real installed plugin instead of a mocked route.
// Content fixtures under e2e/fixtures/faq-content/ are copied verbatim from
// ut-plugin-faq's real content/ bundles (not synthetic test data).
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/universaltill/universal-till/internal/db"
)

const (
	pluginID      = "com.universaltill.ut-faq"
	pluginVersion = "0.2.3"
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
	dbPath := filepath.Join(dataDir, "unitill-pos.db")

	conn, err := db.Open(dbPath)
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
		pluginID, pluginVersion, "Universal Till FAQ", "", "", "", "none", "", "local-e2e-fixture",
		strings.Repeat("0", 64), "1.0.0", "1.0")

	exec(`INSERT OR IGNORE INTO plugins (id, name, version, entrypoint, runtime)
		VALUES (?,?,?,?,?)`,
		pluginID, "Universal Till FAQ", pluginVersion, "", "none")

	exec(`INSERT OR IGNORE INTO plugin_entries (id, plugin_id, type, key, label, route, menu_group)
		VALUES (?,?,?,?,?,?,?)`,
		"e2e-faq-page", pluginID, "page", "faq-page", "plugin.faq.menu", "/plugin/faq", "help_support")

	if err := tx.Commit(); err != nil {
		fatalf("commit: %v", err)
	}

	pluginDir := filepath.Join(dataDir, "plugins", pluginID, pluginVersion)
	// run-till.sh cd's to the repo root before invoking `go run ./e2e/seed_faq`.
	// locales/ is real too (copied from ut-plugin-faq's locales/{en,fa}.json)
	// so internal/plugins.Manager.syncLocales picks up the real
	// "plugin.faq.menu" translation the same way a real install would,
	// instead of the page falling back to the untranslated key.
	copyFixtureDir("e2e/fixtures/faq-content", filepath.Join(pluginDir, "content"))
	copyFixtureDir("e2e/fixtures/faq-locales", filepath.Join(pluginDir, "locales"))

	fmt.Println("Seeded FAQ plugin for e2e")
}

func copyFixtureDir(srcDir, dstDir string) {
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		fatalf("mkdir %s: %v", dstDir, err)
	}
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		fatalf("read fixtures %s: %v", srcDir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if err := copyFile(filepath.Join(srcDir, e.Name()), filepath.Join(dstDir, e.Name())); err != nil {
			fatalf("copy fixture %s: %v", e.Name(), err)
		}
	}
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}
