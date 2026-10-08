package plugins

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/paths"
)

// ut-docs#3160: a plugin view's text key must come from the plugin's OWN
// locale bundle. syncLocales records, per plugin, the keys its own
// locales/*.json declare (any locale), refreshed on every sync.
func TestOwnLocaleKeys_PerPlugin_3160(t *testing.T) {
	dataDir := t.TempDir()
	prev := paths.DataDir()
	paths.Init(dataDir)
	t.Cleanup(func() { paths.Init(prev) })

	db := managerTestDB(t)
	ctx := context.Background()
	seedInstalledPlugin(t, db, "com.test.a", "A", "1.0.0", "none", true)
	seedInstalledPlugin(t, db, "com.test.b", "B", "1.0.0", "none", true)
	write := func(id, name, body string) {
		dir := filepath.Join(dataDir, "plugins", id, "1.0.0", "locales")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("com.test.a", "en.json", `{"plugin.a.title":"A"}`)
	write("com.test.a", "de.json", `{"plugin.a.de_only":"nur"}`)
	write("com.test.b", "en.json", `{"plugin.b.title":"B"}`)

	m, err := Init(ctx, &config.Config{Env: "test"}, db)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	m.SetLocalizer(&captureLocalizer{})

	a := m.OwnLocaleKeys("com.test.a")
	if !a["plugin.a.title"] || !a["plugin.a.de_only"] {
		t.Fatalf("own keys of a = %v, want both its keys", a)
	}
	if a["plugin.b.title"] {
		t.Fatal("another plugin's key counted as a's own")
	}
	if len(m.OwnLocaleKeys("com.test.none")) != 0 {
		t.Fatal("unknown plugin has keys")
	}

	// Refreshed on the next sync.
	write("com.test.a", "en.json", `{"plugin.a.new":"N"}`)
	if err := m.Reload(ctx); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if a := m.OwnLocaleKeys("com.test.a"); !a["plugin.a.new"] || a["plugin.a.title"] {
		t.Fatalf("own keys not refreshed on sync: %v", a)
	}
}
