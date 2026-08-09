package pos

import (
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
