package plugins

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/data"
)

// Review finding (ut-docs#6565): an ACTIVE wasm row the App Store loader
// refuses (a join snapshot or a restored DB carrying another till's tax or
// fiscal plugin) must be marked install_state='broken' like any other load
// failure, or the tax fail-closed check (ut-docs#368) cannot see it and the
// sale silently falls back to base rates.
func TestAppStore_RefusedActiveWasmRowIsMarkedBroken(t *testing.T) {
	codeGateChannel(t, buildinfo.ChannelAppStore)
	SetBundledCodeVerifier(nil)
	raw, err := os.ReadFile(buildHostfnGuest(t))
	if err != nil {
		t.Fatal(err)
	}
	db := managerTestDB(t)
	base := t.TempDir()
	seedInstalledPlugin(t, db, "com.test.refusedtax", "T", "1.0.0", "wasm", true)
	dir := filepath.Join(base, "com.test.refusedtax", "1.0.0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "run"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	w := NewWasmRuntime(base)
	t.Cleanup(func() { w.Close(context.Background()) })
	w.Sync(context.Background(), db)

	rows, err := data.NewPluginRepo(db).ListInstalledPlugins(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.ID == "com.test.refusedtax" {
			if r.InstallState != data.PluginStateBroken {
				t.Fatalf("install_state = %q, want %q: a refused active plugin must be visible to the fail-closed checks", r.InstallState, data.PluginStateBroken)
			}
			return
		}
	}
	t.Fatal("seeded row not found")
}

// Review finding (ut-docs#6565): the App Store loader verifies the very bytes
// it compiles. The verifier here swaps the file for garbage the moment it has
// been asked; the module must still load, from the bytes that were verified,
// and those bytes must be the file's content at read time.
func TestAppStore_LoaderCompilesExactlyTheVerifiedBytes(t *testing.T) {
	codeGateChannel(t, buildinfo.ChannelAppStore)
	raw, err := os.ReadFile(buildHostfnGuest(t))
	if err != nil {
		t.Fatal(err)
	}
	db := managerTestDB(t)
	base := t.TempDir()
	seedInstalledPlugin(t, db, "com.test.toctou", "T", "1.0.0", "wasm", true)
	dir := filepath.Join(base, "com.test.toctou", "1.0.0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	mod := filepath.Join(dir, "run")
	if err := os.WriteFile(mod, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	var calls int
	var sawFileBytes bool
	SetBundledCodeVerifier(func(id, version, path string, wasm []byte) bool {
		calls++
		sawFileBytes = string(wasm) == string(raw)
		// The swap an attacker would race in after a verify-by-path.
		if err := os.WriteFile(mod, []byte("not wasm at all"), 0o644); err != nil {
			t.Errorf("swap: %v", err)
		}
		return sawFileBytes
	})
	t.Cleanup(func() { SetBundledCodeVerifier(nil) })
	w := NewWasmRuntime(base)
	t.Cleanup(func() { w.Close(context.Background()) })
	w.Sync(context.Background(), db)

	if calls == 0 || !sawFileBytes {
		t.Fatalf("verifier calls = %d, saw the file's bytes = %v", calls, sawFileBytes)
	}
	w.mu.Lock()
	_, ok := w.modules["com.test.toctou"]
	w.mu.Unlock()
	if !ok {
		t.Fatal("the module did not load from the verified bytes (the file was read again after the check)")
	}
}

// Review finding: PersistManifest can land a plugin disabled in its own
// transaction (InstallOptions.InstallInactive), so no committed state ever
// has it enabled.
func TestPersistManifest_InstallInactiveCommitsDisabled(t *testing.T) {
	codeGateChannel(t, buildinfo.ChannelDirect)
	db := managerTestDB(t)
	ctx := context.Background()
	m := &Manifest{ID: "com.test.inactive", Name: "I", Version: "1.0.0", Entrypoint: "./p.wasm", Runtime: "wasm", CanonicalType: "page", DeviceArch: "any"}
	if err := PersistManifest(ctx, db, m, InstallOptions{TrustLevel: "system", InstallInactive: true}); err != nil {
		t.Fatal(err)
	}
	repo := data.NewPluginRepo(db)
	if on, err := repo.PluginActive(ctx, m.ID); err != nil || on {
		t.Fatalf("active = %v, %v; want a disabled row", on, err)
	}
	// The default is unchanged: every other install still activates.
	if err := PersistManifest(ctx, db, m, InstallOptions{TrustLevel: "system"}); err != nil {
		t.Fatal(err)
	}
	if on, err := repo.PluginActive(ctx, m.ID); err != nil || !on {
		t.Fatalf("active = %v, %v; want the default install to activate", on, err)
	}
}

// Review finding (ut-docs#6565): rollback restores a snapshot's files and
// moves the row to that version without going through PersistManifest, so it
// needs its own App Store gate. A code plugin is never rolled back there (the
// bundled set moves only with an app release); other channels are unchanged.
func TestAppStore_RollbackOfCodePluginRefused_OtherChannelsUnchanged(t *testing.T) {
	setup := func(t *testing.T) (*RollbackManager, *sql.DB, string) {
		d := openRealDB(t)
		ctx := context.Background()
		base := t.TempDir()
		restore := buildinfo.OverrideChannelForTest(buildinfo.ChannelDirect)
		v2 := &Manifest{ID: "com.rb.gate", Name: "RB", Version: "2.0.0", Entrypoint: "./main.wasm", Runtime: "wasm", CanonicalType: "page", DeviceArch: "any"}
		err := PersistManifest(ctx, d.DB, v2, InstallOptions{})
		restore()
		if err != nil {
			t.Fatalf("install v2: %v", err)
		}
		mustExecSQL(t, d, `INSERT INTO plugin_catalog (id, version, name, runtime, entrypoint, package_url, sha256, min_pos_version, api_version, published_at)
VALUES ('com.rb.gate', '1.0.0', 'RB', 'wasm', './main.wasm', 'https://example.invalid', 'deadbeef', '0.0.1', '1', '2026-07-30T00:00:00Z')`)
		dir := filepath.Join(base, "com.rb.gate", "versions", "1.0.0")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		m := `{"id":"com.rb.gate","name":"RB","version":"1.0.0","entrypoint":"./main.wasm","runtime":"wasm","canonical_type":"page","device_arch":"any"}`
		if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(m), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "main.wasm"), []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}
		return NewRollbackManager(d.DB, base), d.DB, base
	}
	version := func(t *testing.T, db *sql.DB) string {
		v, _, err := data.NewPluginRepo(db).GetInstalledPluginVersion(context.Background(), "com.rb.gate")
		if err != nil {
			t.Fatal(err)
		}
		return v
	}

	t.Run("appstore refused", func(t *testing.T) {
		rm, db, base := setup(t)
		codeGateChannel(t, buildinfo.ChannelAppStore)
		err := rm.Rollback(context.Background(), "com.rb.gate", "1.0.0", "tester")
		if !errors.Is(err, ErrCodePluginRefused) {
			t.Fatalf("err = %v, want ErrCodePluginRefused", err)
		}
		if v := version(t, db); v != "2.0.0" {
			t.Fatalf("version = %s, a refused rollback moved the row", v)
		}
		if _, err := os.Stat(filepath.Join(base, "com.rb.gate", "1.0.0")); !os.IsNotExist(err) {
			t.Fatalf("a refused rollback restored a live dir (stat err = %v)", err)
		}
	})
	t.Run("direct unchanged", func(t *testing.T) {
		rm, db, _ := setup(t)
		codeGateChannel(t, buildinfo.ChannelDirect)
		if err := rm.Rollback(context.Background(), "com.rb.gate", "1.0.0", "tester"); err != nil {
			t.Fatalf("rollback on direct: %v", err)
		}
		if v := version(t, db); v != "1.0.0" {
			t.Fatalf("version = %s, want 1.0.0", v)
		}
	})
}
