package plugins

import (
	"context"
	"sync"
	"testing"
	"time"
)

// Regression guard for ut-docs#503 (a regression from ut-docs#380/PR#268):
// Manager.Close (→ WasmRuntime.Close → EventBus.ResetSubscribers) closes
// every subscriber channel EventBus.publish sends on. publish
// (internal/plugins/ipc.go) snapshots its subscriber slice under RLock,
// releases the lock, does a per-subscriber CheckPermission DB query, and
// only THEN sends on the channel — so calling Close while a publisher could
// still be inside that window is a real "send on closed channel" panic.
//
// PR#268 originally called Manager.Close from server.Start's
// <-ctx.Done()-triggered shutdown goroutine — the SAME cancellation signal
// that independently triggers every other background publisher on the
// caller's wg (internal/cloudsync's ticker, in particular), with nothing
// sequencing Close to wait for those publishers to actually stop first.
// The fix (this card) moves the call into app.Run's own deferred cleanup,
// strictly after drainBackgroundServices has joined every wg member —
// by which point every publisher this till boots has actually exited, not
// merely been asked to.
//
// This test proves the SAFE half directly against the real production
// call path (SharedBus, real Subscribe/Publish, real Manager.Close): a
// live publisher racing 300 subscribe cycles, each followed by properly
// draining that publisher (mirroring drainBackgroundServices) BEFORE
// Close runs, must never panic. The dangerous half (calling Close
// WHILE the publisher is still live) was verified manually against this
// exact call path during review — see the code-review record — and is not
// exercised here as a permanent test, since deliberately triggering an
// unrecovered panic would crash this test binary rather than fail cleanly;
// the compile-time removal of server.Start's pluginManager parameter (see
// internal/server/server.go) is what actually prevents the dangerous call
// site from silently reappearing there.
func TestManagerClose_NeverPanicsWhenSequencedAfterPublisherDrain(t *testing.T) {
	db := managerTestDB(t)
	ctx := context.Background()
	const pid = "com.test.closeseq"
	seedInstalledPlugin(t, db, pid, "CloseSeq", "1.0.0", "wasm", true)
	if _, err := db.Exec(`INSERT INTO plugin_permissions (id, plugin_id, permission, granted) VALUES ('cs1', ?, 'events:receive', 1)`, pid); err != nil {
		t.Fatalf("perm: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active) VALUES ('csh1', ?, 'close.seq', 'noop', 1)`, pid); err != nil {
		t.Fatalf("hook: %v", err)
	}

	bus := SharedBus(db) // matches production: Manager.Close → WasmRuntime.Close → SharedBus(db).ResetSubscribers
	w := NewWasmRuntime(t.TempDir())
	w.db = db
	m := &Manager{Wasm: w, db: db}

	for i := 0; i < 300; i++ {
		// A wg-registered background publisher, exactly like
		// internal/cloudsync's ticker: keeps publishing until told to stop.
		var publisherWg sync.WaitGroup
		stop := make(chan struct{})
		var stopOnce sync.Once
		drain := func() {
			stopOnce.Do(func() { close(stop) })
			publisherWg.Wait()
		}
		publisherWg.Add(1)
		go func() {
			defer publisherWg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_, _ = bus.Publish(ctx, "close.seq", map[string]any{"i": i})
			}
		}()
		// Safety net for ut-docs#750's flake class (same shape as
		// ut-docs#509): if bus.Subscribe below t.Fatalf's, Goexit would skip
		// the manual drain() call further down, leaking this iteration's
		// publisher goroutine into whatever test runs next — closed-DB audit
		// writes from the leak then starve the shared logger's mutex.
		// Registered every iteration on purpose: on the normal (non-Fatal)
		// path each one just becomes a no-op at test end (stopOnce/Wait both
		// already settled) — 300 cheap no-ops, not a real accumulation, and
		// simpler than threading a single end-of-test drain list through the
		// loop for a case that should never actually fire.
		// t.Cleanup + sync.Once make the eventual drain idempotent with the
		// manual call on the success path, which must stay exactly where it
		// is — draining before m.Close is the behavior this test asserts.
		t.Cleanup(drain)

		if _, err := bus.Subscribe(ctx, pid, []string{"close.seq"}); err != nil {
			t.Fatalf("subscribe: %v", err)
		}
		time.Sleep(time.Millisecond) // let the publisher actually be mid-flight

		// The fix's invariant, mirrored exactly: stop and DRAIN the
		// publisher (app.Run's drainBackgroundServices) BEFORE Close runs
		// (app.Run's pluginManager.Close, in its deferred cleanup).
		drain()
		m.Close(context.Background())
	}
	// Reaching here without a panic is the assertion — the loop's whole
	// point is width against timing flakiness in either direction.
}
