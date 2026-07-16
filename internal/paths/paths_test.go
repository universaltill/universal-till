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
	MigrateLegacyData(target)
	b, err := os.ReadFile(target)
	if err != nil || string(b) != "legacy" {
		t.Fatalf("migrate failed: err=%v content=%q", err, b)
	}

	// Idempotent: a second call must not clobber an existing target.
	_ = os.WriteFile(target, []byte("current"), 0o644)
	MigrateLegacyData(target)
	if b, _ := os.ReadFile(target); string(b) != "current" {
		t.Fatalf("migrate clobbered existing db: %q", b)
	}
}

func TestMigrateLegacyPlugins(t *testing.T) {
	wd := t.TempDir()
	old, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(old); Init("") })
	_ = os.Chdir(wd)

	// Legacy tree: one installed plugin (with a nested, executable file) and
	// transient dirs that must not migrate.
	mustWrite := func(path, content string, mode os.FileMode) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(filepath.Join("data", "plugins", "com.example.faq", "0.2.2", "locales", "en.json"), `{"k":"v"}`, 0o644)
	mustWrite(filepath.Join("data", "plugins", "com.example.faq", "0.2.2", "bin", "plugin"), "bin", 0o755)
	mustWrite(filepath.Join("data", "plugins", "cache", "snapshot.json"), "{}", 0o644)
	mustWrite(filepath.Join("data", "plugins", "tmp", "part"), "x", 0o644)

	stable := filepath.Join(wd, "stable")
	Init(stable)
	// The stable plugins dir already exists with only transient content — the
	// exact state a DB-only migration left behind.
	if err := os.MkdirAll(filepath.Join(stable, "plugins", "cache"), 0o755); err != nil {
		t.Fatal(err)
	}

	MigrateLegacyData(filepath.Join(stable, "unitill-pos.db"))

	if b, err := os.ReadFile(filepath.Join(stable, "plugins", "com.example.faq", "0.2.2", "locales", "en.json")); err != nil || string(b) != `{"k":"v"}` {
		t.Fatalf("plugin bundle not migrated: err=%v content=%q", err, b)
	}
	if info, err := os.Stat(filepath.Join(stable, "plugins", "com.example.faq", "0.2.2", "bin", "plugin")); err != nil || info.Mode()&0o100 == 0 {
		t.Fatalf("executable bit lost: err=%v mode=%v", err, info.Mode())
	}
	if _, err := os.Stat(filepath.Join(stable, "plugins", "tmp")); err == nil {
		t.Fatal("transient tmp dir must not migrate")
	}

	// Idempotent: an existing bundle in the stable dir is never overwritten.
	mustWrite(filepath.Join(stable, "plugins", "com.example.faq", "0.2.2", "locales", "en.json"), "current", 0o644)
	MigrateLegacyData(filepath.Join(stable, "unitill-pos.db"))
	if b, _ := os.ReadFile(filepath.Join(stable, "plugins", "com.example.faq", "0.2.2", "locales", "en.json")); string(b) != "current" {
		t.Fatalf("migration clobbered existing bundle: %q", b)
	}
}
