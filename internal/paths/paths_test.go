package paths

import (
	"os"
	"path/filepath"
	"runtime"
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

// DataDir falls back to the pre-refactor ./data layout until Init records a
// real location.
func TestDataDirFallback(t *testing.T) {
	t.Cleanup(func() { Init("") })
	Init("")
	if got := DataDir(); got != "./data" {
		t.Fatalf("DataDir with no Init = %q, want ./data", got)
	}
	Init("/srv/ut")
	if got := DataDir(); got != "/srv/ut" {
		t.Fatalf("DataDir after Init = %q", got)
	}
}

// Default resolves the conventional per-user data dir for the current OS and
// falls back to ./data when no home can be resolved. Branches for other OSes
// run on their own CI platforms.
func TestDefault(t *testing.T) {
	home := t.TempDir()
	switch runtime.GOOS {
	case "windows":
		t.Setenv("LOCALAPPDATA", home)
		if got, want := Default(), filepath.Join(home, "UniversalTill"); got != want {
			t.Fatalf("Default = %q, want %q", got, want)
		}
		t.Setenv("LOCALAPPDATA", "")
		if got := Default(); got != "./data" {
			t.Fatalf("Default without LOCALAPPDATA = %q, want ./data", got)
		}
	case "darwin":
		t.Setenv("HOME", home)
		if got, want := Default(), filepath.Join(home, "Library", "Application Support", "UniversalTill"); got != want {
			t.Fatalf("Default = %q, want %q", got, want)
		}
		t.Setenv("HOME", "")
		if got := Default(); got != "./data" {
			t.Fatalf("Default without HOME = %q, want ./data", got)
		}
	default: // linux, bsd, …
		t.Setenv("XDG_DATA_HOME", home)
		if got, want := Default(), filepath.Join(home, "universal-till"); got != want {
			t.Fatalf("Default = %q, want %q", got, want)
		}
		t.Setenv("XDG_DATA_HOME", "")
		t.Setenv("HOME", home)
		if got, want := Default(), filepath.Join(home, ".local", "share", "universal-till"); got != want {
			t.Fatalf("Default without XDG = %q, want %q", got, want)
		}
		t.Setenv("HOME", "")
		if got := Default(); got != "./data" {
			t.Fatalf("Default without HOME = %q, want ./data", got)
		}
	}
}

// The DB migration's guard branches: still-legacy config, no legacy DB,
// an unmakeable target dir, and an unreadable legacy DB must all leave the
// world untouched (best-effort, never fatal).
func TestMigrateLegacyDBGuards(t *testing.T) {
	wd := t.TempDir()
	old, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(old) })
	_ = os.Chdir(wd)

	// No legacy DB at all → nothing appears at the target.
	target := filepath.Join(wd, "stable", "unitill-pos.db")
	migrateLegacyDB(target)
	if _, err := os.Stat(target); err == nil {
		t.Fatal("target created with no legacy db present")
	}

	// Still configured onto the legacy path (both spellings): exercised for
	// line coverage; behaviourally subsumed by the target-exists guard (with
	// dbPath == legacy, "target" already has the database), so no observable
	// difference can be asserted beyond the legacy file staying intact.
	if err := os.MkdirAll("data", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("data", "unitill-pos.db"), []byte("legacy"), 0o644); err != nil {
		t.Fatal(err)
	}
	migrateLegacyDB("data/unitill-pos.db")
	migrateLegacyDB("./data/unitill-pos.db")
	if b, _ := os.ReadFile(filepath.Join("data", "unitill-pos.db")); string(b) != "legacy" {
		t.Fatalf("legacy db modified: %q", b)
	}

	// Target dir can't be created (parent is a file) → no panic, no target,
	// and the blocking file is left exactly as it was.
	if err := os.WriteFile("blocked", []byte("f"), 0o644); err != nil {
		t.Fatal(err)
	}
	migrateLegacyDB(filepath.Join("blocked", "sub", "unitill-pos.db"))
	if _, err := os.Stat(filepath.Join("blocked", "sub")); err == nil {
		t.Fatal("a directory was created under the blocking file")
	}
	if info, err := os.Stat("blocked"); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("blocking file disturbed: err=%v", err)
	}

	// Legacy DB unreadable → stat passes, open fails, target stays absent.
	if runtime.GOOS == "windows" {
		t.Skip("chmod 0o000 does not block reads on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores file modes")
	}
	if err := os.Chmod(filepath.Join("data", "unitill-pos.db"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join("data", "unitill-pos.db"), 0o644) })
	migrateLegacyDB(target)
	if _, err := os.Stat(target); err == nil {
		t.Fatal("target created from an unreadable legacy db")
	}
}

// copyFileBestEffort never errors out loud and never half-copies.
func TestCopyFileBestEffort(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the failing-read case relies on unix EISDIR semantics")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	dst := filepath.Join(dir, "dst.txt")

	// Missing source → no destination.
	copyFileBestEffort(filepath.Join(dir, "nope.txt"), dst)
	if _, err := os.Stat(dst); err == nil {
		t.Fatal("dst created from a missing src")
	}

	if err := os.WriteFile(src, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Uncreatable destination (missing parent dir) → silent no-op.
	copyFileBestEffort(src, filepath.Join(dir, "missing-dir", "dst.txt"))

	// Happy path.
	copyFileBestEffort(src, dst)
	if b, err := os.ReadFile(dst); err != nil || string(b) != "content" {
		t.Fatalf("copy failed: err=%v content=%q", err, b)
	}

	// A read that fails mid-copy (a directory opens fine but EISDIRs on
	// read) must remove the truncated destination, not leave it behind.
	srcDir := filepath.Join(dir, "srcdir")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	half := filepath.Join(dir, "half.txt")
	copyFileBestEffort(srcDir, half)
	if _, err := os.Stat(half); err == nil {
		t.Fatal("truncated destination left behind after a failed copy")
	}
}

// Plugin migration's guard branches: still-legacy location, stray files,
// a blocked target tree, and a failed bundle copy (which must clean up the
// half-copied bundle rather than leave it looking installed).
func TestMigrateLegacyPluginsGuards(t *testing.T) {
	wd := t.TempDir()
	old, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(old); Init("") })
	_ = os.Chdir(wd)

	mustWrite := func(path, content string, mode os.FileMode) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(filepath.Join("data", "plugins", "com.example.faq", "manifest.json"), "{}", 0o644)
	mustWrite(filepath.Join("data", "plugins", "stray-file"), "x", 0o644)

	// Still using the legacy location (target == legacy): exercised for line
	// coverage; with dst == src every bundle also hits the already-present
	// skip, so the observable claim is only that the legacy tree is untouched.
	Init("data")
	migrateLegacyPlugins()
	if b, _ := os.ReadFile(filepath.Join("data", "plugins", "com.example.faq", "manifest.json")); string(b) != "{}" {
		t.Fatalf("legacy tree modified by same-location migrate: %q", b)
	}

	// Target plugins dir blocked by a file → per-bundle mkdir fails, no panic.
	blocked := filepath.Join(wd, "blocked")
	Init(blocked)
	mustWrite(filepath.Join(blocked, "plugins"), "not a dir", 0o644)
	migrateLegacyPlugins()

	// Normal target: bundle copies, the stray file does not.
	stable := filepath.Join(wd, "stable")
	Init(stable)
	migrateLegacyPlugins()
	if _, err := os.Stat(filepath.Join(stable, "plugins", "com.example.faq", "manifest.json")); err != nil {
		t.Fatalf("bundle not migrated: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stable, "plugins", "stray-file")); err == nil {
		t.Fatal("stray file must not migrate")
	}

	// A bundle whose copy fails half-way is removed, not left half-installed.
	if runtime.GOOS == "windows" {
		t.Skip("chmod 0o000 does not block reads on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores file modes")
	}
	mustWrite(filepath.Join("data", "plugins", "com.example.broken", "locked", "f"), "x", 0o644)
	if err := os.Chmod(filepath.Join("data", "plugins", "com.example.broken", "locked"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join("data", "plugins", "com.example.broken", "locked"), 0o755) })
	migrateLegacyPlugins()
	if _, err := os.Stat(filepath.Join(stable, "plugins", "com.example.broken")); err == nil {
		t.Fatal("half-copied bundle must be removed")
	}
}

// Uploaded-assets migration edge branches: a stray top-level file under
// items/, an existing target logo that must never be clobbered, a blocked
// items target, and a failed item-dir copy that must clean up after itself.
func TestMigrateLegacyUploadedAssetsGuards(t *testing.T) {
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
	mustWrite(filepath.Join("web", "public", "assets", "items", "loose.png"), "loose")
	// Named to sort AFTER loose.png: the file branch must create the target
	// items dir itself (its own os.MkdirAll), not inherit one a preceding
	// dir entry made — the repo's recurring missing-MkdirAll bug class.
	mustWrite(filepath.Join("web", "public", "assets", "items", "z-itm1", "thumb.png"), "photo")
	mustWrite(filepath.Join("web", "public", "assets", "logo", "receipt-logo.png"), "old-logo")

	// Target items path blocked by a file → mkdir fails, no panic, logo still
	// migrates (independent best-effort steps).
	blocked := filepath.Join(wd, "blocked")
	Init(blocked)
	if err := os.MkdirAll(filepath.Join(blocked, "public", "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(filepath.Join(blocked, "public", "assets", "items"), "not a dir")
	migrateLegacyUploadedAssets()
	if b, err := os.ReadFile(filepath.Join(blocked, "public", "assets", "logo", "receipt-logo.png")); err != nil || string(b) != "old-logo" {
		t.Fatalf("logo not migrated despite blocked items dir: err=%v %q", err, b)
	}

	// A stray top-level FILE under items/ migrates via the file branch; an
	// already-present target logo is never overwritten.
	stable := filepath.Join(wd, "stable")
	Init(stable)
	mustWrite(filepath.Join(stable, "public", "assets", "logo", "receipt-logo.png"), "current-logo")
	migrateLegacyUploadedAssets()
	if b, err := os.ReadFile(filepath.Join(stable, "public", "assets", "items", "loose.png")); err != nil || string(b) != "loose" {
		t.Fatalf("top-level file not migrated: err=%v %q", err, b)
	}
	if b, _ := os.ReadFile(filepath.Join(stable, "public", "assets", "logo", "receipt-logo.png")); string(b) != "current-logo" {
		t.Fatalf("existing target logo clobbered: %q", b)
	}

	// An item dir whose copy fails half-way is removed, not left half-copied.
	if runtime.GOOS == "windows" {
		t.Skip("chmod 0o000 does not block reads on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores file modes")
	}
	mustWrite(filepath.Join("web", "public", "assets", "items", "itmX", "locked", "f"), "x")
	if err := os.Chmod(filepath.Join("web", "public", "assets", "items", "itmX", "locked"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join("web", "public", "assets", "items", "itmX", "locked"), 0o755) })
	fresh := filepath.Join(wd, "fresh")
	Init(fresh)
	migrateLegacyUploadedAssets()
	if _, err := os.Stat(filepath.Join(fresh, "public", "assets", "items", "itmX")); err == nil {
		t.Fatal("half-copied item dir must be removed")
	}
}
