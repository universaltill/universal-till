package bundled

import (
	"context"
	"os"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/paths"
)

// Review finding (ut-docs#6565): the verifier judges the bytes it is handed,
// never a (path, size, mtime) memo of an earlier read. Before the fix it
// stat'ed the path and reused a cached hash, so a same-size swap with the
// modification time put back was accepted.
func TestVerify_SameSizeSwapAtTheLockedPathIsRefused(t *testing.T) {
	_ = openTestDB(t)
	_, lock := fixture(t, "1.0.0")
	p := paths.Plugins(fakeID, "1.0.0", "bin", "plugin.wasm")
	good, evil := []byte("\x00asm-fake-1.0.0"), []byte("\x00asm-EVIL-1.0.0")
	if len(evil) != len(good) {
		t.Fatal("fixture: the swap must keep the size")
	}
	if !verifyAgainst(lock, fakeID, "1.0.0", p, good) {
		t.Fatal("the locked bytes were not accepted")
	}
	if verifyAgainst(lock, fakeID, "1.0.0", p, evil) {
		t.Fatal("same-size bytes that are not the locked ones were accepted")
	}
	if !verifyAgainst(lock, fakeID, "1.0.0", p, good) {
		t.Fatal("the locked bytes were refused after a refusal (no state may be kept)")
	}
}

// Review finding: a same-version boot heals EVERY locked file, not only the
// wasm. Before the fix an altered or deleted manifest or locale file was left
// as it was.
func TestSync_RewritesAlteredManifestAndLocaleOfSameVersion(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	fsys, lock := fixture(t, "1.0.0")
	if _, err := syncFrom(ctx, d.DB, fsys, lock); err != nil {
		t.Fatal(err)
	}
	manifest := paths.Plugins(fakeID, "1.0.0", "manifest.json")
	locale := paths.Plugins(fakeID, "1.0.0", "locales", "en.json")
	want, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, []byte(`{"id":"tampered"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(locale); err != nil {
		t.Fatal(err)
	}
	if changed, err := syncFrom(ctx, d.DB, fsys, lock); err != nil || !changed {
		t.Fatalf("sync = %v, %v; want the altered files rewritten", changed, err)
	}
	if got, _ := os.ReadFile(manifest); string(got) != string(want) {
		t.Fatalf("manifest after resync = %q", got)
	}
	if got, _ := os.ReadFile(locale); string(got) != `{"a":"b"}` {
		t.Fatalf("locale after resync = %q", got)
	}
}

// Review finding: "off by default" must hold from the very first committed
// row. The plugin upsert writes is_active = 1, so the first install has to
// land disabled inside the same transaction: an app killed between an
// "install" and a separate "disable" would otherwise come back with a payment
// plugin switched on with no keys (its no-credentials demo path approves).
func TestSync_FirstInstallNeverCommitsAnActiveRow(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	fsys, lock := fixture(t, "1.0.0")
	var sawActive bool
	afterPersist = func() {
		on, err := data.NewPluginRepo(d.DB).PluginActive(ctx, fakeID)
		if err != nil {
			t.Errorf("read enabled state: %v", err)
		}
		sawActive = on
	}
	t.Cleanup(func() { afterPersist = nil })
	if _, err := syncFrom(ctx, d.DB, fsys, lock); err != nil {
		t.Fatal(err)
	}
	if sawActive {
		t.Fatal("the bundled plugin was committed enabled before being switched off")
	}
}
