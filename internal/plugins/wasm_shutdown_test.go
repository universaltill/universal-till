package plugins

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
)

// WasmRuntime.Shutdown must stop the per-plugin event-channel drainer
// goroutines Sync spawned and not return until they have exited
// (ut-docs#380). This drives the REAL path end to end: Sync twice (the
// second Sync exercises the cross-generation Add/Done accounting —
// ResetSubscribers closes the first generation's channels while the second
// generation registers), prove a drainer is actually live by publishing a
// non-blocking event and watching the guest handle it (the hostfn guest
// writes a "results" key to plugin storage on every event), then Shutdown
// and assert it returns promptly — i.e. via the drainers actually exiting,
// not via its timeout bound.
func TestWasmRuntime_Shutdown_StopsEventDrainers(t *testing.T) {
	guest := buildHostfnGuest(t)

	db := managerTestDB(t)
	ctx := context.Background()
	base := t.TempDir()
	w := NewWasmRuntime(base)

	const pluginID = "com.test.drainwasm"
	seedInstalledPlugin(t, db, pluginID, "DrainWasm", "1.0.0", "wasm", true)
	if _, err := db.Exec(`UPDATE plugins SET entrypoint = './plugin.wasm' WHERE id = ?`, pluginID); err != nil {
		t.Fatalf("set entrypoint: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO plugin_permissions (id, plugin_id, permission, granted) VALUES
		('dw1', ?, 'events:receive', 1), ('dw2', ?, 'storage', 1)`, pluginID, pluginID); err != nil {
		t.Fatalf("seed permissions: %v", err)
	}
	// A non-blocking event: delivery goes through the subscriber channel,
	// so the drainer goroutine is what actually runs the handler.
	if _, err := db.Exec(`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active) VALUES
		('dwh1', ?, 'test.drain.ping', 'guest.handle', 1)`, pluginID); err != nil {
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
	// A second Sync mid-life (a plugin install/uninstall reload): the first
	// generation's drainer exits via ResetSubscribers, a fresh one is
	// registered — Shutdown below still joins cleanly, no panic, no wedge.
	w.Sync(ctx, db)

	bus := SharedBus(db)
	if !bus.HasSubscribers("test.drain.ping") {
		t.Fatal("hook event not subscribed after Sync")
	}

	// Prove Shutdown actually JOINS the drainer, not just that it stops it
	// eventually: publish, then call Shutdown IMMEDIATELY — no wait, no
	// poll — while the real wasm dispatch (module instantiate + guest
	// run + storage_set host call) for this event is still plausibly
	// in flight on the drainer goroutine. If Shutdown merely closed the
	// channel and returned without waiting on drainWg (the exact bug this
	// join fixes — verified during review to leave both this test's old
	// shape and the timeout test green even with the real join deleted
	// from Sync), the result below would be racy-absent; because Shutdown
	// blocks on drainWg.Wait() until the drainer's handle() call — and so
	// its storage_set — has actually returned, it's deterministic here.
	repo := data.NewPluginRepo(db)
	if _, err := bus.Publish(ctx, "test.drain.ping", map[string]any{"q": "ping"}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	start := time.Now()
	testStart := start
	w.Shutdown(5 * time.Second)
	// Well under the bound = the drainers really exited; hitting ~5s would
	// mean Shutdown only returned because its timeout gave up on them.
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Shutdown took %s, want well under its 5s bound — drainers did not exit on channel close", elapsed)
	}
	// Asserted the INSTANT Shutdown returns, no wait: this is the join
	// itself, not eventual consistency.
	if raw, err := repo.StorageGet(ctx, pluginID, "results"); err != nil || len(raw) == 0 {
		t.Fatal("Shutdown returned before the drainer's in-flight event was actually handled — the goroutine was not joined")
	}
	for _, p := range logging.Recent() {
		if p.Level == "ERROR" && p.At.After(testStart) && strings.Contains(p.Msg, "event drainer goroutines still running") {
			t.Fatalf("Shutdown hit its timeout instead of joining the drainers: %s", p.Msg)
		}
	}
	// The subscriptions are gone with the drainers.
	if bus.HasSubscribers("test.drain.ping") {
		t.Fatal("subscriptions survived Shutdown")
	}
	// And a Sync after Shutdown must not quietly re-subscribe channels
	// nothing will ever drain again.
	w.Sync(ctx, db)
	if bus.HasSubscribers("test.drain.ping") {
		t.Fatal("Sync after Shutdown re-subscribed a closed runtime")
	}
}

// A drainer that never exits (a handler wedged mid-event) must not hang
// Shutdown forever: the join is bounded, and giving up is logged loudly.
// Same shape as app's
// TestDrainBackgroundServices_TimesOutAndLogsWhenWgNeverCompletes.
func TestWasmRuntime_Shutdown_TimesOutAndLogsWhenDrainerNeverExits(t *testing.T) {
	w := NewWasmRuntime(t.TempDir())
	w.drainWg.Add(1)          // deliberately never Done() — simulates a wedged drainer
	t.Cleanup(w.drainWg.Done) // let the internal Wait goroutine finish after the test

	start := time.Now()
	w.Shutdown(100 * time.Millisecond)
	elapsed := time.Since(start)
	if elapsed < 100*time.Millisecond {
		t.Fatalf("Shutdown returned after %s, before its own 100ms bound elapsed", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("Shutdown took %s, want close to its 100ms bound", elapsed)
	}

	found := false
	for _, p := range logging.Recent() {
		if p.Level == "ERROR" && p.At.After(start) && strings.Contains(p.Msg, "event drainer goroutines still running") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected an ERROR log noting event drainer goroutines were still running after the bound")
	}
}
