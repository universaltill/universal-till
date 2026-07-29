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

// TestMigrateLegacyUploadedAssets guards the fix for a real bug: item/
// variant photos and the receipt logo used to be written into the release
// tree (web/public/assets), which a self-update replaces wholesale — a
// shop's uploads vanished on the very next update. This migrates anything
// already sitting in the old location into the stable data dir, once, so
// fixing the write path doesn't ALSO require every shop to re-upload
// everything right after finally getting the fix.
func TestMigrateLegacyUploadedAssets(t *testing.T) {
	wd := t.TempDir()
	old, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(old); Init("") })
	_ = os.Chdir(wd)

	mustWrite := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// An item photo, a variant photo nested under it, and the receipt logo —
	// all in the legacy, cwd-relative release-tree location.
	mustWrite(filepath.Join("web", "public", "assets", "items", "itm1", "thumb.png"), "item-photo")
	mustWrite(filepath.Join("web", "public", "assets", "items", "itm1", "variants", "v1", "thumb.png"), "variant-photo")
	mustWrite(filepath.Join("web", "public", "assets", "logo", "receipt-logo.png"), "logo-bytes")
	// A BUILT-IN default asset that must NOT be swept into the stable dir —
	// only items/ and the specific receipt-logo.png file are user content.
	mustWrite(filepath.Join("web", "public", "assets", "logo", "ut-logo.ico"), "built-in-icon")

	stable := filepath.Join(wd, "stable")
	Init(stable)
	MigrateLegacyData(filepath.Join(stable, "unitill-pos.db"))

	if b, err := os.ReadFile(filepath.Join(stable, "public", "assets", "items", "itm1", "thumb.png")); err != nil || string(b) != "item-photo" {
		t.Fatalf("item photo not migrated: err=%v content=%q", err, b)
	}
	if b, err := os.ReadFile(filepath.Join(stable, "public", "assets", "items", "itm1", "variants", "v1", "thumb.png")); err != nil || string(b) != "variant-photo" {
		t.Fatalf("variant photo not migrated: err=%v content=%q", err, b)
	}
	if b, err := os.ReadFile(filepath.Join(stable, "public", "assets", "logo", "receipt-logo.png")); err != nil || string(b) != "logo-bytes" {
		t.Fatalf("receipt logo not migrated: err=%v content=%q", err, b)
	}
	if _, err := os.Stat(filepath.Join(stable, "public", "assets", "logo", "ut-logo.ico")); err == nil {
		t.Fatal("built-in default asset must NOT be migrated into the stable dir — it would shadow future releases' updates to it")
	}

	// Idempotent: an existing file in the stable dir is never overwritten.
	mustWrite(filepath.Join(stable, "public", "assets", "items", "itm1", "thumb.png"), "current")
	MigrateLegacyData(filepath.Join(stable, "unitill-pos.db"))
	if b, _ := os.ReadFile(filepath.Join(stable, "public", "assets", "items", "itm1", "thumb.png")); string(b) != "current" {
		t.Fatalf("migration clobbered an existing uploaded photo: %q", b)
	}
}
