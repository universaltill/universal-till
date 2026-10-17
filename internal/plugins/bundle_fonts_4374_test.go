package plugins

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/paths"
)

// ADR-0160 §8 / ut-docs#4374: a language bundle may ship a font subset in
// fonts/ (woff2). Only an ACTIVE plugin whose manifest says canonical_type
// "language" contributes fonts; only plain-named *.woff2 regular files count.
func TestBundleFonts_ActiveLanguagePluginsOnly_4374(t *testing.T) {
	dataDir := t.TempDir()
	prev := paths.DataDir()
	paths.Init(dataDir)
	t.Cleanup(func() { paths.Init(prev) })

	db := managerTestDB(t)
	ctx := context.Background()
	seedInstalledPlugin(t, db, "com.test.lang-th", "Thai", "2.0.0", "none", true)
	seedInstalledPlugin(t, db, "com.test.lang-ja", "Japanese", "1.0.0", "none", true)
	seedInstalledPlugin(t, db, "com.test.theme", "Theme", "1.0.0", "none", true)
	seedInstalledPlugin(t, db, "com.test.lang-off", "Disabled", "1.0.0", "none", false)

	write := func(id, version, rel, body string) {
		t.Helper()
		p := filepath.Join(dataDir, "plugins", id, version, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	lang := `{"canonical_type":"language","runtime":"none"}`
	write("com.test.lang-th", "2.0.0", "manifest.json", lang)
	write("com.test.lang-th", "2.0.0", "fonts/NotoSansThai-Subset.woff2", "wOF2")
	write("com.test.lang-th", "2.0.0", "fonts/readme.txt", "not a font")
	write("com.test.lang-th", "2.0.0", "fonts/Evil.ttf", "not woff2")
	write("com.test.lang-th", "2.0.0", "fonts/bad name.woff2", "space in name")
	write("com.test.lang-th", "2.0.0", "fonts/nested/Inner.woff2", "nested")
	write("com.test.lang-th", "2.0.0", "fonts/Fake.woff2", "<svg onload=x>") // no wOF2 signature
	write("com.test.lang-th", "2.0.0", "fonts/Short.woff2", "wO")
	write("com.test.lang-ja", "1.0.0", "manifest.json", lang)
	write("com.test.lang-ja", "1.0.0", "fonts/NotoSansJP-Subset.woff2", "wOF2")
	// Not a language plugin: its fonts never reach the global stack.
	write("com.test.theme", "1.0.0", "manifest.json", `{"canonical_type":"theme","runtime":"none"}`)
	write("com.test.theme", "1.0.0", "fonts/Spoof.woff2", "wOF2")
	// Disabled language plugin: not loaded.
	write("com.test.lang-off", "1.0.0", "manifest.json", lang)
	write("com.test.lang-off", "1.0.0", "fonts/Off.woff2", "wOF2")
	// A directory named like a font is not a regular file.
	if err := os.MkdirAll(filepath.Join(dataDir, "plugins", "com.test.lang-ja", "1.0.0", "fonts", "Dir.woff2"), 0o755); err != nil {
		t.Fatal(err)
	}

	m, err := Init(ctx, &config.Config{Env: "test"}, db)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	want := []BundleFont{
		{PluginID: "com.test.lang-ja", Version: "1.0.0", File: "NotoSansJP-Subset.woff2"},
		{PluginID: "com.test.lang-th", Version: "2.0.0", File: "NotoSansThai-Subset.woff2"},
	}
	if got := m.BundleFonts(); !reflect.DeepEqual(got, want) {
		t.Fatalf("BundleFonts() = %+v\nwant %+v", got, want)
	}

	// The returned slice is a copy: a caller can't corrupt the manager's set.
	got := m.BundleFonts()
	got[0].File = "x.woff2"
	if m.BundleFonts()[0].File != "NotoSansJP-Subset.woff2" {
		t.Fatal("BundleFonts must return a copy")
	}

	// Disabling a language plugin drops its fonts on the next reload.
	if _, err := db.Exec(`UPDATE plugins SET is_active = 0 WHERE id = 'com.test.lang-ja'`); err != nil {
		t.Fatal(err)
	}
	if err := m.Reload(ctx); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	want = want[1:]
	if got := m.BundleFonts(); !reflect.DeepEqual(got, want) {
		t.Fatalf("after disable: BundleFonts() = %+v\nwant %+v", got, want)
	}
}

func TestBundleFonts_NilManager_4374(t *testing.T) {
	var m *Manager
	if got := m.BundleFonts(); got != nil {
		t.Fatalf("nil manager: got %+v", got)
	}
}
