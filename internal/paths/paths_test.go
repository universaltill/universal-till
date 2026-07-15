package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDataAndPlugins(t *testing.T) {
	Init("/srv/ut")
	if got := Data("unitill-pos.db"); got != filepath.Join("/srv/ut", "unitill-pos.db") {
		t.Fatalf("Data = %q", got)
	}
	if got := Plugins("cache"); got != filepath.Join("/srv/ut", "plugins", "cache") {
		t.Fatalf("Plugins = %q", got)
	}
}

func TestMigrateLegacyDB(t *testing.T) {
	wd := t.TempDir()
	old, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(old) })
	_ = os.Chdir(wd)

	// Legacy DB present, target absent → migrate.
	if err := os.MkdirAll("data", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("data", "unitill-pos.db"), []byte("legacy"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(wd, "stable", "unitill-pos.db")
	MigrateLegacyDB(target)
	b, err := os.ReadFile(target)
	if err != nil || string(b) != "legacy" {
		t.Fatalf("migrate failed: err=%v content=%q", err, b)
	}

	// Idempotent: a second call must not clobber an existing target.
	_ = os.WriteFile(target, []byte("current"), 0o644)
	MigrateLegacyDB(target)
	if b, _ := os.ReadFile(target); string(b) != "current" {
		t.Fatalf("migrate clobbered existing db: %q", b)
	}
}
