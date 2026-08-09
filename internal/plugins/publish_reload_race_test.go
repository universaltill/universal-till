package plugins

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
)

// Regression guard for ut-docs#504 (the gap ut-docs#503's own review found):
// #503 fixed the publish-vs-ResetSubscribers "send on closed channel" panic
// for the Manager.Close/shutdown path only, by sequencing Close after every
// background publisher drained (internal/app/app.go). But ResetSubscribers
// also runs on EVERY plugin install/uninstall via Manager.Reload →
// WasmRuntime.Sync — a path where live publishers (cloudsync's ticker, any
// in-flight sale) are NOT stopped first, and never can be: installs happen
// while the till is running.
//
// The #504 fix is at the root cause: EventBus.publish now holds eb.mu.RLock
// for its ENTIRE dispatch loop, so ResetSubscribers' exclusive Lock cannot
// close a channel any in-flight publish might still send on. This test
// therefore — deliberately, unlike #503's test — never stops or drains the
// publisher before Reload: a live Publish racing real Manager.Reload calls
// must never panic, with no sequencing discipline required from callers.
func TestPublish_NeverPanicsRacingManagerReload(t *testing.T) {
	db := managerTestDB(t)
	ctx := context.Background()
	const pid = "com.test.reloadrace"
	// "none" runtime: Reload's WasmRuntime.Sync still unconditionally calls
	// ResetSubscribers() even with no wasm module to load — that call path
	// is exactly what's under test.
	seedInstalledPlugin(t, db, pid, "ReloadRace", "1.0.0", "none", true)
	if _, err := db.Exec(`INSERT INTO plugin_permissions (id, plugin_id, permission, granted) VALUES ('rr1', ?, 'events:receive', 1)`, pid); err != nil {
		t.Fatalf("perm: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active) VALUES ('rrh1', ?, 'race.event', 'noop', 1)`, pid); err != nil {
		t.Fatalf("hook: %v", err)
	}

	m, err := Init(ctx, &config.Config{Env: "test"}, db)
	if err != nil {
		t.Fatalf("init: %v", err)
	}

	bus := SharedBus(db)
	if _, err := bus.Subscribe(ctx, pid, []string{"race.event"}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	stop := make(chan struct{})
	var publisherWg sync.WaitGroup
	publisherWg.Add(1)
	go func() {
		defer publisherWg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_, _ = bus.Publish(ctx, "race.event", map[string]any{"x": 1})
			// Deliberately NOT stopping or draining around Reloads — the
			// publisher just keeps calling Publish regardless of
			// subscriber-set churn. That indiscipline being safe is the
			// whole point of the #504 fix.
		}
	}()

	for i := 0; i < 100; i++ {
		if err := m.Reload(ctx); err != nil {
			t.Fatalf("reload %d: %v", i, err)
		}
		// Re-subscribe after each Reload, mirroring what a real wasm
		// plugin's Sync-driven resubscribe does moments after
		// ResetSubscribers() within the same Sync call — keeps a live
		// channel for the publisher to race against on every iteration,
		// not just the first.
		if _, err := bus.Subscribe(ctx, pid, []string{"race.event"}); err != nil {
			t.Fatalf("resubscribe %d: %v", i, err)
		}
	}

	close(stop)
	publisherWg.Wait()
	// Reaching here without a panic (run with -race to also confirm no data
	// race) is the assertion.
}

// Regression guard for a finding from ut-docs#504's own independent review:
// the fix above makes EventBus.publish hold eb.mu.RLock across its whole
// dispatch loop, including a Blocking-mode handler's call (payment
// authorization, ".ask", ".refund" hooks). A Blocking handler that never
// returns — a wedged or malicious plugin — would then hold that RLock
// forever, and since Go's sync.RWMutex blocks NEW readers once a writer is
// pending, one stuck handler would starve every other bus operation
// (ResetSubscribers on the next plugin install, HasSubscribers/Generation on
// the checkout tax-rate-ask path, every other Publish) till process restart —
// turning a localized hang into a till-wide one. The fix: publish releases
// eb.mu specifically around the Blocking handler call (safe: mode is fixed
// for the whole publish() call, so a Blocking-mode dispatch never touches
// sub.Channel either way) and NewWasmRuntime now enables wazero's
// WithCloseOnContextDone so the timeout HandleEvent already computes is
// actually enforced against a real wasm module. This test proves the
// EventBus-side half directly: a Blocking handler blocked on a channel must
// not prevent a CONCURRENT bus operation from completing promptly.
func TestPublish_BlockingHandlerDoesNotWedgeBus(t *testing.T) {
	db := managerTestDB(t)
	ctx := context.Background()
	const pid = "com.test.wedge"
	seedInstalledPlugin(t, db, pid, "Wedge", "1.0.0", "none", true)
	if _, err := db.Exec(`INSERT INTO plugin_permissions (id, plugin_id, permission, granted) VALUES ('w1', ?, 'events:receive', 1)`, pid); err != nil {
		t.Fatalf("perm: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active) VALUES ('wh1', ?, 'wedge.authorize', 'noop', 1)`, pid); err != nil {
		t.Fatalf("hook: %v", err)
	}

	bus := SharedBus(db)
	bus.SetEventMode("wedge.authorize", Blocking)

	handlerEntered := make(chan struct{})
	releaseHandler := make(chan struct{})
	handler := func(ctx context.Context, ev Event) (json.RawMessage, error) {
		close(handlerEntered)
		<-releaseHandler // simulates a wedged/slow plugin: never returns on its own
		return nil, nil
	}
	if _, err := bus.SubscribeWithHandler(ctx, pid, []string{"wedge.authorize"}, handler); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	publishDone := make(chan error, 1)
	go func() {
		_, err := bus.Publish(ctx, "wedge.authorize", map[string]any{"x": 1})
		publishDone <- err
	}()

	select {
	case <-handlerEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("blocking handler never entered")
	}

	// The handler is now blocked inside sub.Handler, mid-Publish. A
	// concurrent bus operation that needs eb.mu must still complete
	// promptly — proving publish is NOT holding the lock across the
	// handler call.
	done := make(chan struct{})
	go func() {
		bus.ResetSubscribers()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ResetSubscribers blocked >2s behind a live Blocking handler — the bus is wedged")
	}

	if has := bus.HasSubscribers("wedge.authorize"); has {
		t.Fatal("ResetSubscribers should have cleared subscribers by now")
	}

	close(releaseHandler)
	select {
	case err := <-publishDone:
		if err != nil {
			t.Fatalf("Publish returned an error after its handler was released: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Publish never returned after its handler was released")
	}
}
