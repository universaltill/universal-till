package plugins

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/logging"
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

// Close (ut-docs#380) must stop the per-plugin event-channel drainer
// goroutines Sync started — by closing their channels via the shared bus —
// and return promptly once they have exited, so app.Run's shutdown drain
// never leaves one blocked forever on `range ch` while the database closes
// under it.
func TestWasmRuntimeClose_StopsDrainersAndReturnsPromptly(t *testing.T) {
	guest := buildHostfnGuest(t)

	db := managerTestDB(t)
	ctx := context.Background()
	base := t.TempDir()
	w := NewWasmRuntime(base)

	const pluginID = "com.test.closewasm"
	seedInstalledPlugin(t, db, pluginID, "CloseWasm", "1.0.0", "wasm", true)
	if _, err := db.Exec(`UPDATE plugins SET entrypoint = './plugin.wasm' WHERE id = ?`, pluginID); err != nil {
		t.Fatalf("set entrypoint: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO plugin_permissions (id, plugin_id, permission, granted) VALUES
		('cw1', ?, 'events:receive', 1)`, pluginID); err != nil {
		t.Fatalf("seed permissions: %v", err)
	}
	// A non-blocking hook event, so Sync starts a real drainer goroutine for
	// this plugin's channel (blocking-mode events run inline instead).
	if _, err := db.Exec(`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active) VALUES
		('cwh1', ?, 'test.close.ping', 'guest.handle', 1)`, pluginID); err != nil {
		t.Fatalf("seed hooks: %v", err)
	}

	raw, err := os.ReadFile(guest)
	if err != nil {
		t.Fatalf("read guest: %v", err)
	}
	modPath := filepath.Join(base, pluginID, "1.0.0", "plugin.wasm")
	if err := writeFileWithParents(modPath, raw); err != nil {
		t.Fatalf("write module: %v", err)
	}

	w.Sync(ctx, db)

	bus := SharedBus(db)
	defer bus.ResetSubscribers()
	if !bus.HasSubscribers("test.close.ping") {
		t.Fatal("hook event not subscribed — Sync did not start a drainer to stop")
	}

	start := time.Now()
	w.Close(ctx) // ctx has no deadline: a prompt return proves a real drain, not a timeout
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("Close took %s — it should return as soon as the drainers exit", elapsed)
	}

	// The subscriptions are gone (their channels were closed)...
	if bus.HasSubscribers("test.close.ping") {
		t.Fatal("subscribers still present after Close")
	}
	// ...and every drainer goroutine has actually exited: the WaitGroup they
	// register on must already be at zero.
	if !wgSettlesWithin(&w.wg, 2*time.Second) {
		t.Fatal("drainer goroutines still registered on the WaitGroup after Close returned")
	}
}

// Close is shutdown plumbing reached through Manager.Close from server.Start,
// where a caller may legitimately hold a nil manager or a runtime that never
// synced — every layer must be nil-safe.
func TestWasmRuntimeClose_NilSafe(t *testing.T) {
	ctx := context.Background()

	var w *WasmRuntime
	w.Close(ctx) // must not panic

	var m *Manager
	m.Close(ctx) // must not panic

	(&Manager{}).Close(ctx) // nil Wasm field — must not panic

	// A runtime that was constructed but never Synced has no db and no
	// drainers; Close must return immediately without touching the shared bus.
	NewWasmRuntime(t.TempDir()).Close(ctx)
}

// A drainer that never exits (a wedged handler holding the channel loop) must
// not hang shutdown forever: Close returns once ctx expires, logging loudly.
func TestWasmRuntimeClose_TimesOutLoudlyOnWedgedDrainer(t *testing.T) {
	w := NewWasmRuntime(t.TempDir())

	// Simulate a wedged drainer goroutine: registered but never finishing
	// (until test cleanup lets the leaked waiter goroutine exit).
	w.wg.Add(1)
	t.Cleanup(w.wg.Done)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	done := make(chan struct{})
	go func() { defer close(done); w.Close(ctx) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Close never returned despite its ctx expiring")
	}
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Fatalf("Close returned after %s, before its ctx deadline — it never actually waited", elapsed)
	}

	found := false
	for _, p := range logging.Recent() {
		if p.Level == "ERROR" && p.At.After(start.Add(-time.Second)) &&
			strings.Contains(p.Msg, "wasm event drainer") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected a loud ERROR log about wasm drainer goroutines still running at shutdown timeout")
	}
}

// wgSettlesWithin reports whether wg.Wait() returns within d.
func wgSettlesWithin(wg *sync.WaitGroup, d time.Duration) bool {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}
