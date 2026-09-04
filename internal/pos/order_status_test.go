package pos

import (
	"encoding/json"
	"testing"
	"time"
)

// The order lifecycle conflict rule (ut-docs#526, extending ADR-0011's
// "conflict rules — fixed, simple" philosophy): status only ever moves
// FORWARD through new(1) < preparing(2) < ready(3) < collected(4); a stale
// write arriving after a later status (offline multi-till catch-up) is
// silently dropped, never an error and never a visible regression. cancelled
// is terminal and reachable from any state except collected/cancelled.

func TestOrderStatusRank(t *testing.T) {
	cases := []struct {
		status string
		want   int
	}{
		{"", 0},
		{OrderStatusNew, 1},
		{OrderStatusPreparing, 2},
		{OrderStatusReady, 3},
		{OrderStatusCollected, 4},
		{OrderStatusCancelled, 0}, // terminal — not on the forward ladder
		{"bogus", 0},
	}
	for _, c := range cases {
		if got := OrderStatusRank(c.status); got != c.want {
			t.Errorf("OrderStatusRank(%q) = %d, want %d", c.status, got, c.want)
		}
	}
}

func TestValidOrderStatus(t *testing.T) {
	for _, s := range []string{OrderStatusNew, OrderStatusPreparing, OrderStatusReady, OrderStatusCollected, OrderStatusCancelled} {
		if !ValidOrderStatus(s) {
			t.Errorf("ValidOrderStatus(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "bogus", "NEW", "done"} {
		if ValidOrderStatus(s) {
			t.Errorf("ValidOrderStatus(%q) = true, want false", s)
		}
	}
}

func TestOrderStatusAllowed(t *testing.T) {
	cases := []struct {
		current, next string
		want          bool
	}{
		// Forward moves apply — including skipping steps (a till that was
		// offline may deliver "ready" for an order nobody marked "preparing").
		{"", OrderStatusNew, true},
		{"", OrderStatusPreparing, true},
		{"", OrderStatusCollected, true},
		{OrderStatusNew, OrderStatusPreparing, true},
		{OrderStatusNew, OrderStatusReady, true},
		{OrderStatusPreparing, OrderStatusReady, true},
		{OrderStatusReady, OrderStatusCollected, true},

		// Same-status repeats are no-ops (idempotent double-tap).
		{OrderStatusNew, OrderStatusNew, false},
		{OrderStatusPreparing, OrderStatusPreparing, false},
		{OrderStatusCollected, OrderStatusCollected, false},

		// Backward moves are silently dropped (stale offline catch-up).
		{OrderStatusPreparing, OrderStatusNew, false},
		{OrderStatusReady, OrderStatusPreparing, false},
		{OrderStatusCollected, OrderStatusReady, false},
		{OrderStatusCollected, OrderStatusNew, false},

		// Cancel is reachable from any non-terminal state...
		{"", OrderStatusCancelled, true},
		{OrderStatusNew, OrderStatusCancelled, true},
		{OrderStatusPreparing, OrderStatusCancelled, true},
		{OrderStatusReady, OrderStatusCancelled, true},
		// ...but not from collected, and cancel-from-cancelled is a no-op.
		{OrderStatusCollected, OrderStatusCancelled, false},
		{OrderStatusCancelled, OrderStatusCancelled, false},

		// Nothing leaves cancelled.
		{OrderStatusCancelled, OrderStatusNew, false},
		{OrderStatusCancelled, OrderStatusPreparing, false},
		{OrderStatusCancelled, OrderStatusCollected, false},

		// Garbage next-status never applies.
		{"", "bogus", false},
		{OrderStatusNew, "", false},
	}
	for _, c := range cases {
		if got := OrderStatusAllowed(c.current, c.next); got != c.want {
			t.Errorf("OrderStatusAllowed(%q, %q) = %v, want %v", c.current, c.next, got, c.want)
		}
	}
}

func TestOrderStatusBroadcaster_SubscribePublish(t *testing.T) {
	b := NewOrderStatusBroadcaster()
	ch, cancel := b.Subscribe()
	defer cancel()

	ev := OrderStatusChanged{ReceiptNo: "R-0001", Status: OrderStatusPreparing, ActorID: "u1", At: "2026-08-09T10:00:00Z"}
	b.Publish(ev)

	select {
	case got := <-ch:
		if got != ev {
			t.Fatalf("received %+v, want %+v", got, ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("subscriber never received the published event")
	}
}

func TestOrderStatusBroadcaster_UnsubscribeStopsDelivery(t *testing.T) {
	b := NewOrderStatusBroadcaster()
	ch, cancel := b.Subscribe()
	cancel()

	// Publishing after unsubscribe must not panic and must not deliver.
	b.Publish(OrderStatusChanged{ReceiptNo: "R-0002", Status: OrderStatusReady})

	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("received an event after unsubscribe")
		}
		// closed channel — fine
	case <-time.After(200 * time.Millisecond):
		// nothing delivered — also fine
	}

	// Double-cancel must be safe.
	cancel()
}

// A slow (never-reading) subscriber must never block Publish: this is a
// latest-state notification, not a delivery-guaranteed queue — events for a
// full subscriber channel are dropped for that subscriber only.
func TestOrderStatusBroadcaster_SlowSubscriberNeverBlocksPublish(t *testing.T) {
	b := NewOrderStatusBroadcaster()
	_, cancelSlow := b.Subscribe() // never read from
	defer cancelSlow()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 1000; i++ { // far beyond any sane buffer size
			b.Publish(OrderStatusChanged{ReceiptNo: "R-0003", Status: OrderStatusPreparing})
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Publish blocked on a slow subscriber")
	}

	// A healthy subscriber added afterwards still receives new events.
	ch, cancel := b.Subscribe()
	defer cancel()
	b.Publish(OrderStatusChanged{ReceiptNo: "R-0004", Status: OrderStatusReady})
	select {
	case got := <-ch:
		if got.ReceiptNo != "R-0004" {
			t.Fatalf("healthy subscriber got %+v, want R-0004", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("healthy subscriber starved by an unrelated slow subscriber")
	}
}

// ADR-0079 (ut-docs#1571): OrderStatusChanged is now also a WIRE type — the
// SSE stream endpoints JSON-encode it verbatim — so it must marshal
// snake_case like every other JSON body this product emits (CLAUDE.md:
// "JSON snake_case"), not as Go field names.
func TestOrderStatusChanged_MarshalsSnakeCase(t *testing.T) {
	b, err := json.Marshal(OrderStatusChanged{ReceiptNo: "R-0001", Status: OrderStatusReady, ActorID: "op-1", At: "2026-09-04T10:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"receipt_no":"R-0001","status":"ready","actor_id":"op-1","at":"2026-09-04T10:00:00Z"}`
	if string(b) != want {
		t.Fatalf("marshal = %s, want %s", b, want)
	}
	var back OrderStatusChanged
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.ReceiptNo != "R-0001" || back.Status != OrderStatusReady || back.ActorID != "op-1" || back.At != "2026-09-04T10:00:00Z" {
		t.Fatalf("round-trip = %+v", back)
	}
}

// SubscriberCount lets the SSE handler tests prove the unsubscribe-on-
// disconnect path actually runs (ADR-0079); Close releases every live
// subscriber at process shutdown so a long-lived stream handler blocked on
// its channel returns instead of holding http.Server.Shutdown open. After
// Close the broadcaster is inert: Publish is a no-op and Subscribe hands
// back an already-closed channel, so a handler racing shutdown exits at once.
func TestOrderStatusBroadcaster_SubscriberCountAndClose(t *testing.T) {
	b := NewOrderStatusBroadcaster()
	if got := b.SubscriberCount(); got != 0 {
		t.Fatalf("fresh broadcaster has %d subscribers, want 0", got)
	}
	_, cancel1 := b.Subscribe()
	ch2, cancel2 := b.Subscribe()
	defer cancel2()
	if got := b.SubscriberCount(); got != 2 {
		t.Fatalf("after two Subscribe: %d, want 2", got)
	}
	cancel1()
	cancel1() // idempotent
	if got := b.SubscriberCount(); got != 1 {
		t.Fatalf("after cancel: %d, want 1", got)
	}

	b.Close()
	if got := b.SubscriberCount(); got != 0 {
		t.Fatalf("after Close: %d subscribers, want 0", got)
	}
	select {
	case _, ok := <-ch2:
		if ok {
			t.Fatal("Close must close the live subscriber channel, got a value instead")
		}
	case <-time.After(time.Second):
		t.Fatal("Close must close the live subscriber channel")
	}
	b.Publish(OrderStatusChanged{ReceiptNo: "R-0009"}) // must not panic
	ch3, cancel3 := b.Subscribe()
	defer cancel3()
	select {
	case _, ok := <-ch3:
		if ok {
			t.Fatal("Subscribe after Close must hand back a closed channel, got a value")
		}
	case <-time.After(time.Second):
		t.Fatal("Subscribe after Close must hand back an already-closed channel")
	}
	if got := b.SubscriberCount(); got != 0 {
		t.Fatalf("Subscribe after Close must not register anything, got %d", got)
	}
}
