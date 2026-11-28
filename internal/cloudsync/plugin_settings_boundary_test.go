package cloudsync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ADR-0214 (ut-docs#6969) named allowance: the till's plugin_settings report
// is built by internal/pluginsettingsreport (never a secret value) and added
// to the check-in by internal/pages/cloudsync_wire.go. THIS package still
// touches no plugin setting, so the check below is unchanged;
// TestPluginSettingsReportHasOneCaller (this file) pins who may import it.
//
// ADR-0082 (ut-docs#1739) no-leak check: till→ut-cloud sync (this package —
// NOT the LAN primary/replica sync in internal/pages/sync_*.go) must never
// touch plugin_settings in any form: not the table, not the repository's
// setting accessors, not the sealed values. The catalog/stock snapshot it
// pushes is built from its own queries, so this pins that boundary by
// reading the package's non-test sources rather than asserting it in a
// comment. If cloud ever legitimately needs a plugin setting, that is a new
// ADR (it would move a merchant credential — sealed or not — off the till).
func TestCloudSyncNeverTouchesPluginSettings(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatal(err)
		}
		checked++
		for _, forbidden := range []string{
			"plugin_settings",          // the table
			"PluginSetting",            // GetPluginSetting/ListPluginSettings/UpsertPluginSetting*/PluginSettingRow
			"NewPluginRepo",            // the repository that owns the table
			"internal/secrets",         // the seal/open primitive itself
			"sealed:v1:",               // the wire prefix
			"stripe_secret", "api_key", // the credentials ADR-0082 exists for
		} {
			if strings.Contains(string(src), forbidden) {
				t.Errorf("%s references %q — cloud sync must never touch plugin settings (ADR-0082)", name, forbidden)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no source files checked — test is looking in the wrong directory")
	}
}

// TestPluginSettingsReportHasOneCaller pins the ADR-0214 allowance: the only
// non-test file that imports the plugin_settings report package is the
// check-in wiring in internal/pages. A second reader of plugin settings for
// the cloud needs the ADR first.
func TestPluginSettingsReportHasOneCaller(t *testing.T) {
	var importers []string
	err := filepath.Walk(filepath.Join("..", "..", "internal"), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		if strings.Contains(filepath.ToSlash(path), "/pluginsettingsreport/") {
			return nil
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if strings.Contains(string(src), "internal/pluginsettingsreport\"") {
			importers = append(importers, filepath.ToSlash(path))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(importers) != 1 || !strings.HasSuffix(importers[0], "internal/pages/cloudsync_wire.go") {
		t.Fatalf("plugin_settings report importers = %v, want only internal/pages/cloudsync_wire.go (ADR-0214)", importers)
	}
}

// TestPluginSettingsWritersAreNamed (design B 4.3): every plugin setting now
// carries a per-field version that the cloud's conflict rule trusts, so a
// write path must not appear unnoticed. Only these files call a
// plugin-settings writer: the repository itself, the settings page (the
// operator's own edit, bumps ver/origin till), the cloud directive wiring
// (pluginsecretapply, stores the cloud's ver), the tax-takeaway import merge
// and the WASM secret_set host function (both till-side edits, bump ver).
// A new writer must be added here deliberately, with its versioning decided.
// (The LAN replica applies primary rows with raw SQL inside internal/data and
// never originates a value: it keeps the primary's ver and origin.)
func TestPluginSettingsWritersAreNamed(t *testing.T) {
	allowed := map[string]bool{
		"internal/data/plugin_repo.go":           true,
		"internal/pages/plugin_settings_page.go": true,
		"internal/pages/cloud_plugin_secret.go":  true,
		"internal/pages/import_page.go":          true,
		"internal/plugins/wasm_secret_set.go":    true,
	}
	callers := []string{".SavePluginSettingsBatch(", ".UpsertPluginSetting(", ".UpsertPluginSettingScoped(", ".MergeAdditiveJSONMapSetting("}
	root := filepath.Join("..", "..")
	var found []string
	err := filepath.Walk(filepath.Join(root, "internal"), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		for _, c := range callers {
			if strings.Contains(string(src), c) {
				rel, _ := filepath.Rel(root, path)
				found = append(found, filepath.ToSlash(rel))
				break
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) == 0 {
		t.Fatal("no writer found: the guard is looking in the wrong place")
	}
	for _, f := range found {
		if !allowed[f] {
			t.Errorf("%s writes plugin settings but is not a named writer (design B 4.3): decide its ver/origin handling and add it to this test", f)
		}
	}
}
