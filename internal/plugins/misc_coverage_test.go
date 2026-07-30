package plugins

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- manifest_verifier.go helpers -------------------------------------------

func TestNewManifestVerifierValidation(t *testing.T) {
	if _, err := NewManifestVerifier("not-hex!"); err == nil || !strings.Contains(err.Error(), "invalid public key hex") {
		t.Fatalf("bad hex: %v", err)
	}
	if _, err := NewManifestVerifier("abcd"); err == nil || !strings.Contains(err.Error(), "invalid public key size") {
		t.Fatalf("short key: %v", err)
	}
	mv, err := NewManifestVerifier("")
	if err != nil || mv.HasPublicKey() {
		t.Fatalf("dev-mode verifier: %v, HasPublicKey=%v", err, mv.HasPublicKey())
	}
	var nilMV *ManifestVerifier
	if nilMV.HasPublicKey() {
		t.Fatalf("nil verifier claims a key")
	}
}

func TestVerifyArtifact(t *testing.T) {
	mv := &ManifestVerifier{}
	path := filepath.Join(t.TempDir(), "artifact.bin")
	content := []byte("artifact content")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := mv.VerifyArtifact(path, sha256Hex(content)); err != nil {
		t.Fatalf("matching checksum rejected: %v", err)
	}
	if err := mv.VerifyArtifact(path, strings.Repeat("0", 64)); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("wrong checksum accepted: %v", err)
	}
	if err := mv.VerifyArtifact(filepath.Join(t.TempDir(), "missing"), "x"); err == nil || !strings.Contains(err.Error(), "failed to open") {
		t.Fatalf("missing artifact accepted: %v", err)
	}
}

func TestVerifyExecutable(t *testing.T) {
	mv := &ManifestVerifier{}
	dir := t.TempDir()

	if err := mv.VerifyExecutable(dir, "missing"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing exec: %v", err)
	}

	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := mv.VerifyExecutable(dir, "subdir"); err == nil || !strings.Contains(err.Error(), "is a directory") {
		t.Fatalf("dir exec: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "plain"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := mv.VerifyExecutable(dir, "plain"); err == nil || !strings.Contains(err.Error(), "not executable") {
		t.Fatalf("non-exec: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "runme"), []byte("x"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := mv.VerifyExecutable(dir, "runme"); err != nil {
		t.Fatalf("real exec rejected: %v", err)
	}
}

// --- installer_marketplace.go helpers ---------------------------------------

func TestMarketplaceHelperFunctions(t *testing.T) {
	if got := normalizeMarketplaceChecksum("  SHA256:ABCDEF  "); got != "abcdef" {
		t.Errorf("normalizeMarketplaceChecksum = %q", got)
	}
	if got := normalizeMarketplaceChecksum("abc"); got != "abc" {
		t.Errorf("normalizeMarketplaceChecksum plain = %q", got)
	}

	for tier, want := range map[string]string{
		"verified": "trusted", "approved": "trusted",
		"unverified": "untrusted", "": "untrusted", "anything": "untrusted",
	} {
		if got := marketplaceTrustTier(tier); got != want {
			t.Errorf("marketplaceTrustTier(%q) = %q, want %q", tier, got, want)
		}
	}

	if got := sanitizePathSegment(`a/b\c:d`); got != "a-b-c-d" {
		t.Errorf("sanitizePathSegment = %q", got)
	}

	if got := minPOSVersion(nil); got != "2.5.0" {
		t.Errorf("minPOSVersion(nil) = %q", got)
	}
	if got := minPOSVersion(&Manifest{}); got != "2.5.0" {
		t.Errorf("minPOSVersion(empty) = %q", got)
	}
	if got := minPOSVersion(&Manifest{MinPOSVersion: "1.2.3"}); got != "1.2.3" {
		t.Errorf("minPOSVersion(set) = %q", got)
	}
}

func TestNewMarketplaceInstallerRequiresConfig(t *testing.T) {
	if _, err := NewMarketplaceInstaller(nil, nil, nil); err == nil || !strings.Contains(err.Error(), "config is required") {
		t.Fatalf("nil config: %v", err)
	}
}

// --- install_status.go ------------------------------------------------------

func TestInstallStatusClearForPlugin(t *testing.T) {
	db := managerTestDB(t)
	ctx := context.Background()
	store := NewInstallStatusStore(db)

	if err := store.Save(ctx, InstallStatusRecord{
		ListingID: "listing-1", PluginID: "com.test.clear", State: InstallStateActive,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := store.Save(ctx, InstallStatusRecord{
		ListingID: "listing-2", PluginID: "com.test.keep", State: InstallStateActive,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Guards: empty id and nil store are no-ops.
	if err := store.ClearForPlugin(ctx, "  "); err != nil {
		t.Fatalf("blank id: %v", err)
	}
	var nilStore *InstallStatusStore
	if err := nilStore.ClearForPlugin(ctx, "com.test.clear"); err != nil {
		t.Fatalf("nil store: %v", err)
	}

	if err := store.ClearForPlugin(ctx, "com.test.clear"); err != nil {
		t.Fatalf("ClearForPlugin: %v", err)
	}
	records, err := store.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if _, gone := records["listing-1"]; gone {
		t.Fatalf("cleared record still listed")
	}
	if _, kept := records["listing-2"]; !kept {
		t.Fatalf("unrelated record was cleared")
	}
}

// --- ipc.go / wasm_runtime.go ----------------------------------------------

func TestEventBusSubscriberBookkeeping(t *testing.T) {
	db := managerTestDB(t)
	bus := NewEventBus(db)

	if bus.HasSubscribers("sale.completed") {
		t.Fatalf("fresh bus has subscribers")
	}

	seedInstalledPlugin(t, db, "com.test.bus", "Bus", "1.0.0", "none", true)
	if _, err := db.Exec(`INSERT INTO plugin_permissions (id, plugin_id, permission, granted) VALUES ('pb1', 'com.test.bus', 'events:receive', 1)`); err != nil {
		t.Fatalf("seed permission: %v", err)
	}
	// Subscribe requires an active hook declared for the event.
	if _, err := db.Exec(`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active) VALUES ('h1', 'com.test.bus', 'sale.completed', 'bus.handle', 1)`); err != nil {
		t.Fatalf("seed hook: %v", err)
	}

	ch, err := bus.Subscribe(context.Background(), "com.test.bus", []string{"sale.completed"})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if !bus.HasSubscribers("sale.completed") {
		t.Fatalf("subscriber not registered")
	}

	// ResetSubscribers closes the channel (drainers exit) and empties the map.
	bus.ResetSubscribers()
	if bus.HasSubscribers("sale.completed") {
		t.Fatalf("subscribers survived reset")
	}
	if _, open := <-ch; open {
		t.Fatalf("subscriber channel not closed by reset")
	}

	// SetDB rebinds the handle used for permission checks.
	db2 := managerTestDB(t)
	bus.SetDB(db2)
	if bus.dbHandle() != db2 {
		t.Fatalf("SetDB did not rebind")
	}
}

func TestSharedBusSingletonRebinds(t *testing.T) {
	db := managerTestDB(t)
	b1 := SharedBus(db)
	if b1 == nil {
		t.Fatalf("nil bus")
	}
	db2 := managerTestDB(t)
	b2 := SharedBus(db2)
	if b1 != b2 {
		t.Fatalf("SharedBus returned different instances")
	}
	if b2.dbHandle() != db2 {
		t.Fatalf("SharedBus did not rebind to the new db")
	}
	// nil db keeps the previous handle.
	b3 := SharedBus(nil)
	if b3.dbHandle() != db2 {
		t.Fatalf("SharedBus(nil) clobbered the handle")
	}
}

// TestWasmRuntimeSyncLifecycle drives Sync against a real migrated DB: a
// non-wasm plugin must be skipped, a wasm plugin whose module file is missing
// must fail to load without breaking the sync, and a previously-loaded module
// for a now-inactive plugin must be dropped.
func TestWasmRuntimeSyncLifecycle(t *testing.T) {
	db := managerTestDB(t)
	ctx := context.Background()
	base := t.TempDir()
	w := NewWasmRuntime(base)

	seedInstalledPlugin(t, db, "com.test.native", "Native", "1.0.0", "none", true)
	seedInstalledPlugin(t, db, "com.test.wasmmissing", "WasmMissing", "1.0.0", "wasm", true)

	w.Sync(ctx, db)
	if len(w.modules) != 0 {
		t.Fatalf("modules loaded despite missing files: %v", w.modules)
	}

	// A module loaded for a plugin that then went away must be evicted by the
	// next Sync. Inject a real (trivial, empty) compiled wasm module.
	minimalWasm := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00} // "\0asm" v1, no sections
	compiled, err := w.rt.CompileModule(ctx, minimalWasm)
	if err != nil {
		t.Fatalf("compile minimal module: %v", err)
	}
	w.mu.Lock()
	w.modules["com.test.gone"] = compiled
	w.versions["com.test.gone"] = "1.0.0"
	w.hasNet["com.test.gone"] = true
	w.mu.Unlock()

	w.Sync(ctx, db)

	w.mu.Lock()
	_, hasMod := w.modules["com.test.gone"]
	_, hasVer := w.versions["com.test.gone"]
	_, hasNet := w.hasNet["com.test.gone"]
	w.mu.Unlock()
	if hasMod || hasVer || hasNet {
		t.Fatalf("stale module state survived sync: mod=%v ver=%v net=%v", hasMod, hasVer, hasNet)
	}

	// A nil runtime is a no-op, and HandleEvent on an unloaded module errors.
	var nilRT *WasmRuntime
	nilRT.Sync(ctx, db)
	if _, err := w.HandleEvent(ctx, "com.test.unloaded", Event{Type: "x"}); err == nil || !strings.Contains(err.Error(), "module not loaded") {
		t.Fatalf("unloaded module: %v", err)
	}
}

// --- wasm_hostfns.go helpers ------------------------------------------------

func TestHostAllowedScheme(t *testing.T) {
	cases := map[string]bool{
		"https://api.example.com/x": true,
		"http://localhost:8080/x":   true,
		"http://127.0.0.1/x":        true,
		"http://[::1]:11434/x":      true,
		"http://example.com/x":      false,
		"ftp://example.com/x":       false,
	}
	for raw, want := range cases {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("parse %s: %v", raw, err)
		}
		if got := hostAllowedScheme(u); got != want {
			t.Errorf("hostAllowedScheme(%s) = %v, want %v", raw, got, want)
		}
	}
}

func TestPluginHasNetPermission(t *testing.T) {
	db := managerTestDB(t)
	ctx := context.Background()

	seedInstalledPlugin(t, db, "com.test.net", "Net", "1.0.0", "wasm", true)
	if _, err := db.Exec(`INSERT INTO plugin_permissions (id, plugin_id, permission, granted) VALUES
		('n1', 'com.test.net', 'net:api.example.com', 1)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	seedInstalledPlugin(t, db, "com.test.nonet", "NoNet", "1.0.0", "wasm", true)
	if _, err := db.Exec(`INSERT INTO plugin_permissions (id, plugin_id, permission, granted) VALUES
		('n2', 'com.test.nonet', 'net:api.example.com', 0),
		('n3', 'com.test.nonet', 'storage', 1)`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if !pluginHasNetPermission(ctx, db, "com.test.net") {
		t.Fatalf("granted net permission not detected")
	}
	if pluginHasNetPermission(ctx, db, "com.test.nonet") {
		t.Fatalf("ungranted net permission detected")
	}
	if pluginHasNetPermission(ctx, db, "com.test.absent") {
		t.Fatalf("unknown plugin has net permission")
	}
}

// --- telemetry_client.go ----------------------------------------------------

func TestLoadOptInStatus(t *testing.T) {
	db := managerTestDB(t)
	ctx := context.Background()
	tc := NewTelemetryClient(db, "http://127.0.0.1:0", "d", "m", "s")

	// Unset → disabled.
	if err := tc.loadOptInStatus(ctx); err != nil {
		t.Fatalf("loadOptInStatus: %v", err)
	}
	if tc.optInEnabled {
		t.Fatalf("opt-in defaulted to enabled")
	}

	for value, want := range map[string]bool{"true": true, "1": true, "false": false, "0": false, "yes": false} {
		if _, err := db.Exec(`INSERT INTO settings (key, value) VALUES ('marketplace.telemetry_opt_in', ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, value); err != nil {
			t.Fatalf("set setting: %v", err)
		}
		if err := tc.loadOptInStatus(ctx); err != nil {
			t.Fatalf("loadOptInStatus(%s): %v", value, err)
		}
		if tc.optInEnabled != want {
			t.Errorf("opt-in %q → %v, want %v", value, tc.optInEnabled, want)
		}
	}
}
