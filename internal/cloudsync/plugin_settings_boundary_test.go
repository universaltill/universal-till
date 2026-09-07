package cloudsync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
