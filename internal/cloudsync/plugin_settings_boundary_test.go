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
