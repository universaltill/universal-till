package plugins

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestWasmRuntimeSyncLoadsRealModuleAndSubscribes drives Sync end-to-end with
// a REAL wasip1 module (the hostfn guest fixture): the module is compiled and
// registered, hook events are subscribed on the shared bus, and ".ask"
// events are switched to blocking dispatch.
func TestWasmRuntimeSyncLoadsRealModuleAndSubscribes(t *testing.T) {
	guest := buildHostfnGuest(t)

	db := managerTestDB(t)
	ctx := context.Background()
	base := t.TempDir()
	w := NewWasmRuntime(base)

	const pluginID = "com.test.syncwasm"
	seedInstalledPlugin(t, db, pluginID, "SyncWasm", "1.0.0", "wasm", true)
	if _, err := db.Exec(`UPDATE plugins SET entrypoint = './plugin.wasm' WHERE id = ?`, pluginID); err != nil {
		t.Fatalf("set entrypoint: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO plugin_permissions (id, plugin_id, permission, granted) VALUES
		('sw1', ?, 'events:receive', 1), ('sw2', ?, 'storage', 1)`, pluginID, pluginID); err != nil {
		t.Fatalf("seed permissions: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active) VALUES
		('swh1', ?, 'test.sync.ping', 'guest.handle', 1),
		('swh2', ?, 'test.sync.ask', 'guest.answer', 1),
		('swh3', ?, 'payment.test.refund', 'guest.handle', 1)`, pluginID, pluginID, pluginID); err != nil {
		t.Fatalf("seed hooks: %v", err)
	}

	// Lay the real module out where Sync expects it.
	raw, err := os.ReadFile(guest)
	if err != nil {
		t.Fatalf("read guest: %v", err)
	}
	modPath := filepath.Join(base, pluginID, "1.0.0", "plugin.wasm")
	if err := writeFileWithParents(modPath, raw); err != nil {
		t.Fatalf("write module: %v", err)
	}

	w.Sync(ctx, db)

	w.mu.Lock()
	_, loaded := w.modules[pluginID]
	version := w.versions[pluginID]
	w.mu.Unlock()
	if !loaded || version != "1.0.0" {
		t.Fatalf("module not loaded: loaded=%v version=%q", loaded, version)
	}

	bus := SharedBus(db)
	defer bus.ResetSubscribers() // stop drainers before the test DB closes
	if !bus.HasSubscribers("test.sync.ping") || !bus.HasSubscribers("test.sync.ask") || !bus.HasSubscribers("payment.test.refund") {
		t.Fatalf("hook events not subscribed")
	}
	// ".ask" events run blocking (the caller waits on the answer).
	if bus.GetEventMode("test.sync.ask") != Blocking {
		t.Fatalf("ask event not blocking")
	}
	if bus.GetEventMode("test.sync.ping") == Blocking {
		t.Fatalf("plain event unexpectedly blocking")
	}
	// ".refund" events run blocking too (the refund's payment leg waits on
	// the plugin's decline/approve answer — ut-docs#434).
	if bus.GetEventMode("payment.test.refund") != Blocking {
		t.Fatalf("refund event not blocking")
	}

	// A second Sync with the same version keeps the compiled module (the
	// same-version fast path) and re-subscribes cleanly.
	w.Sync(ctx, db)
	w.mu.Lock()
	_, stillLoaded := w.modules[pluginID]
	w.mu.Unlock()
	if !stillLoaded {
		t.Fatalf("module dropped on re-sync")
	}

	// The real blocking path: publish the ".ask" event and let the REAL wasm
	// module execute synchronously (the guest reads the event from stdin and
	// exits 0; its storage/http probes are its own business).
	if _, err := bus.Publish(ctx, "test.sync.ask", map[string]any{"q": "ping"}); err != nil {
		t.Fatalf("blocking publish through real module: %v", err)
	}
}
