package plugins

import (
	"context"
	"testing"
	"time"
)

// Regression guard for ut-docs#674: investigating the reported CI resource
// contention found that EventBus.publish's "channel full" path performed a
// synchronous audit DB write AND a raw stdout print on EVERY dropped event,
// completely unthrottled — one existing regression test
// (TestPublish_NeverPanicsRacingManagerReload), which deliberately hammers
// Publish with no backoff, was measured producing ~16,700 of each in under
// 16 seconds. That volume of synchronous SQLite writes and stdout writes,
// serialized through one connection/mutex, is a genuine, unbounded resource
// amplifier — plausibly related to the symptoms ut-docs#674 reported
// (goroutines blocked on internal/logging's output mutex and database/sql's
// connection opener), though independent review could not actually
// reproduce that CI incident (a double `go test ./...` run, in several
// patterns) to confirm this as its root cause. Fixed here as worthwhile
// hardening regardless.
//
// This test proves shouldWarnChannelFull's throttle directly: back-to-back
// calls within the window collapse to one, and a call after the window
// elapses fires again.
func TestShouldWarnChannelFull_Throttles(t *testing.T) {
	eb := &EventBus{}

	if !eb.shouldWarnChannelFull("com.test.throttle") {
		t.Fatal("first call should warn")
	}
	for i := 0; i < 500; i++ {
		if eb.shouldWarnChannelFull("com.test.throttle") {
			t.Fatalf("call %d within the throttle window should not warn again", i)
		}
	}

	time.Sleep(channelFullWarnInterval + 20*time.Millisecond)
	if !eb.shouldWarnChannelFull("com.test.throttle") {
		t.Fatal("call after the throttle window elapsed should warn again")
	}

	// A different plugin has its own independent window.
	if !eb.shouldWarnChannelFull("com.test.other") {
		t.Fatal("a different plugin ID must not be throttled by another plugin's window")
	}
}

// TestEventBus_Publish_ChannelFullDiagnosticThrottled proves the throttle
// through the real Publish() path (not just the helper in isolation): a
// publisher racing far past a subscriber's channel capacity (100, see
// subscribe()) must not produce one "dropped" audit row per drop — only a
// bounded number, per shouldWarnChannelFull's window.
func TestEventBus_Publish_ChannelFullDiagnosticThrottled(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	setupAuditLog(t, db)

	ctx := context.Background()
	const pid = "com.test.channelfull"

	manifest := &Manifest{
		ID:         pid,
		Name:       "Channel Full Test",
		Version:    "1.0.0",
		Entrypoint: "./test",
		Hooks: []ManifestHook{
			{Event: "race.event", Action: "test.onRace"},
		},
		Permissions: []string{"events:receive"},
	}
	if err := PersistManifest(ctx, db, manifest, InstallOptions{}); err != nil {
		t.Fatalf("persist manifest: %v", err)
	}
	if err := GrantPermission(ctx, db, pid, "events:receive"); err != nil {
		t.Fatalf("grant permission: %v", err)
	}

	bus := NewEventBus(db)
	if _, err := bus.Subscribe(ctx, pid, []string{"race.event"}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	// Deliberately never drained: the channel (capacity 100, see subscribe())
	// fills fast and every publish after that is a "channel full" drop.

	countDropped := func() int {
		t.Helper()
		var dropped int
		if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM audit_log
WHERE action = 'event_dispatch' AND entity_id = ? AND data_json LIKE '%status=dropped%'
`, pid).Scan(&dropped); err != nil {
			t.Fatalf("count dropped audit rows: %v", err)
		}
		return dropped
	}

	const publishes = 400
	for i := 0; i < publishes; i++ {
		if _, err := bus.Publish(ctx, "race.event", map[string]any{"i": i}); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}

	// At least 300 of the 400 publishes above hit the full channel (capacity
	// 100), so without throttling this would be ~300 rows within this single
	// burst (it completes in well under channelFullWarnInterval). The
	// throttle must coalesce all of them into exactly one.
	if dropped := countDropped(); dropped != 1 {
		t.Fatalf("expected exactly 1 throttled 'dropped' audit row for a burst of %d publishes within one window, got %d", publishes, dropped)
	}

	// After the window elapses, a fresh drop must fire a new diagnostic —
	// proving the throttle resets rather than permanently silencing a
	// plugin after its first drop.
	time.Sleep(channelFullWarnInterval + 20*time.Millisecond)
	if _, err := bus.Publish(ctx, "race.event", map[string]any{"i": publishes}); err != nil {
		t.Fatalf("publish after window: %v", err)
	}
	if dropped := countDropped(); dropped != 2 {
		t.Fatalf("expected a second 'dropped' audit row after the throttle window elapsed, got %d", dropped)
	}
}
