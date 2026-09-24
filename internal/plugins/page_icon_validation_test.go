package plugins

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Install-time guard for ut-docs#1734 (follow-up from #1722): a type:"page"
// entry's own declared default menu-tile icon (IconName) must be one of
// uislot.KnownIconNames — the same closed set httpx.IconNames() draws SVGs
// from. Same two call sites as every other page-entry validator in this
// package (PersistManifest, Rollback), but a pure static enum check, not a
// DB collision check.

// pageIconManifest is a page-entry manifest with an explicit icon_name, for
// tests that need only the icon field to vary.
func pageIconManifest(id, key, iconName string) *Manifest {
	return &Manifest{
		ID:         id,
		Name:       "Page " + id,
		Version:    "1.0.0",
		Entrypoint: "./main.wasm",
		Entries: []ManifestEntry{
			{Type: "page", Key: key, Label: "Page " + key, Route: "/plugin/" + key, IconName: iconName},
		},
	}
}

func TestPersistManifest_ValidPageIconNameInstallsCleanly(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()

	if err := PersistManifest(ctx, d.DB, pageIconManifest("com.icon.good", "goodicon", "tag"), InstallOptions{}); err != nil {
		t.Fatalf("a valid icon_name must install cleanly: %v", err)
	}
	var got string
	if err := d.DB.QueryRow(`SELECT icon_path FROM plugin_entries WHERE plugin_id = 'com.icon.good' AND key = 'goodicon'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "tag" {
		t.Fatalf("expected the page entry's icon_path column to hold the declared icon_name %q, got %q", "tag", got)
	}
}

func TestPersistManifest_RejectsInvalidPageIconName(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()

	m := pageIconManifest("com.icon.bad", "badicon", "not-a-real-icon")
	err := PersistManifest(ctx, d.DB, m, InstallOptions{})
	if err == nil {
		t.Fatal("PersistManifest accepted a page entry with an unknown icon_name")
	}
	if !strings.Contains(err.Error(), "not-a-real-icon") || !strings.Contains(err.Error(), "badicon") {
		t.Fatalf("error should name the entry key and the bad icon_name, got: %v", err)
	}
	// Nothing must have persisted — same "rejected plugin left no rows"
	// shape as every other install-time rejection test in this package.
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM plugins WHERE id = 'com.icon.bad'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("rejected plugin left %d plugins row(s), want 0 (transaction must roll back)", n)
	}
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM plugin_entries WHERE plugin_id = 'com.icon.bad'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("rejected plugin left %d plugin_entries row(s), want 0", n)
	}
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM plugin_catalog WHERE id = 'com.icon.bad'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("rejected plugin left %d plugin_catalog row(s), want 0", n)
	}
}

// A button entry's IconPath (a real file path, e.g. "icons/card.svg") must
// never be checked against the page icon-name enum — the two fields are
// deliberately independent (IconName is page-only, IconPath is button-only).
func TestPersistManifest_ButtonIconPathUnaffectedByPageIconValidation(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()

	m := &Manifest{
		ID:         "com.icon.button",
		Name:       "Button plugin",
		Version:    "1.0.0",
		Entrypoint: "./main.wasm",
		Entries: []ManifestEntry{
			{Type: "button", Key: "mybutton", Label: "My Button", ParentPageKey: "sales", TargetAction: "do.thing", IconPath: "icons/card.svg"},
		},
	}
	if err := PersistManifest(ctx, d.DB, m, InstallOptions{}); err != nil {
		t.Fatalf("a button entry's file-path IconPath must never be validated against the page icon-name enum: %v", err)
	}
	var got string
	if err := d.DB.QueryRow(`SELECT icon_path FROM plugin_entries WHERE plugin_id = 'com.icon.button' AND key = 'mybutton'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "icons/card.svg" {
		t.Fatalf("expected the button entry's icon_path preserved verbatim, got %q", got)
	}
}

func TestRollback_CarriesPageIconNameThrough(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()
	base := t.TempDir()

	v2 := pageIconManifest("com.rb.icon", "rbicon", "tag")
	v2.Version = "2.0.0"
	if err := PersistManifest(ctx, d.DB, v2, InstallOptions{}); err != nil {
		t.Fatalf("install v2: %v", err)
	}
	mustExecSQL(t, d, `INSERT INTO plugin_catalog (id, version, name, runtime, entrypoint, package_url, sha256, min_pos_version, api_version, published_at)
VALUES ('com.rb.icon', '1.0.0', 'RB Icon', 'wasm', './main.wasm', 'https://example.invalid', 'deadbeef', '0.0.1', '1', '2026-07-30T00:00:00Z')`)

	dir := filepath.Join(base, "com.rb.icon", "versions", "1.0.0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"id":"com.rb.icon","name":"RB Icon","version":"1.0.0","entrypoint":"./main.wasm","runtime":"wasm",
"entries":[{"type":"page","key":"rbicon","label":"RB Icon Page","route":"/plugin/rbicon","icon_name":"scissors"}]}`
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	rm := NewRollbackManager(d.DB, base)
	if err := rm.Rollback(ctx, "com.rb.icon", "1.0.0", "tester"); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	var got string
	if err := d.DB.QueryRow(`SELECT icon_path FROM plugin_entries WHERE plugin_id = 'com.rb.icon' AND key = 'rbicon'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "scissors" {
		t.Fatalf("rollback must carry the target version's icon_name through, got %q, want %q", got, "scissors")
	}
}

func TestRollback_RejectsInvalidPageIconName(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()
	base := t.TempDir()

	v2 := pageIconManifest("com.rb.badicon", "rbbadicon", "tag")
	v2.Version = "2.0.0"
	if err := PersistManifest(ctx, d.DB, v2, InstallOptions{}); err != nil {
		t.Fatalf("install v2: %v", err)
	}
	mustExecSQL(t, d, `INSERT INTO plugin_catalog (id, version, name, runtime, entrypoint, package_url, sha256, min_pos_version, api_version, published_at)
VALUES ('com.rb.badicon', '1.0.0', 'RB Bad Icon', 'wasm', './main.wasm', 'https://example.invalid', 'deadbeef', '0.0.1', '1', '2026-07-30T00:00:00Z')`)

	dir := filepath.Join(base, "com.rb.badicon", "versions", "1.0.0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A legacy on-disk manifest predating this validation could carry an
	// icon_name no longer (or never) in the closed set.
	manifest := `{"id":"com.rb.badicon","name":"RB Bad Icon","version":"1.0.0","entrypoint":"./main.wasm","runtime":"wasm",
"entries":[{"type":"page","key":"rbbadicon","label":"RB Bad Icon Page","route":"/plugin/rbbadicon","icon_name":"not-a-real-icon"}]}`
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	rm := NewRollbackManager(d.DB, base)
	err := rm.Rollback(ctx, "com.rb.badicon", "1.0.0", "tester")
	if err == nil {
		t.Fatal("rollback restored a page entry with an invalid icon_name")
	}
	if !strings.Contains(err.Error(), "not-a-real-icon") {
		t.Fatalf("rollback rejection should name the bad icon_name, got: %v", err)
	}
	// The current version's entry must survive the failed rollback.
	var got string
	if err := d.DB.QueryRow(`SELECT icon_path FROM plugin_entries WHERE plugin_id = 'com.rb.badicon' AND key = 'rbbadicon'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "tag" {
		t.Fatalf("failed rollback must leave the current entry's icon intact, got %q", got)
	}
}
