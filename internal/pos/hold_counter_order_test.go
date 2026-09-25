package pos

import (
	"encoding/json"
	"testing"
)

// ut-docs#2703: a kiosk pay-at-counter order is parked as a held sale that
// carries the customer's order number ("C-12") and, PER LINE, how much of
// each line already reached the kitchen (a table-QR order prints at
// checkout). Both must survive the payload round trip and a resume ->
// re-park cycle, and must never leak into the next, unrelated sale.
func TestSnapshot_CounterOrderFieldsRoundTrip(t *testing.T) {
	s := newHoldService()
	_, _ = s.Scan("A")
	snap := s.Snapshot()
	if snap.DisplayNo != "" || snap.Lines[0].KitchenSentQty != 0 {
		t.Fatalf("a till-rung basket carries no order number or kitchen-sent qty: %+v", snap)
	}
	snap.DisplayNo = "C-12"
	snap.Lines[0].KitchenSentQty = snap.Lines[0].Qty
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	var back BasketSnapshot
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	s.Reset()
	s.RestoreHeld(back, HeldOrigin{ID: "h1"})
	if s.OrderDisplayNo() != "C-12" {
		t.Fatalf("restored basket lost the order number: %q", s.OrderDisplayNo())
	}
	if got := s.Lines()[0].KitchenSentQty; got != 1 {
		t.Fatalf("restored line kitchen-sent qty = %v, want 1", got)
	}
	// Re-park keeps them.
	again := s.Snapshot()
	if again.DisplayNo != "C-12" || again.Lines[0].KitchenSentQty != 1 {
		t.Fatalf("re-park snapshot dropped the order fields: %+v", again)
	}
	s.Reset()
	if s.OrderDisplayNo() != "" {
		t.Fatal("Reset must clear the order number")
	}
}

// Items added at the till after a table order was sent to the kitchen are
// NOT sent yet: a merged scan raises Qty but not KitchenSentQty, and a new
// line starts at zero -- KitchenPendingQty is exactly what still has to go.
// A line reduced below what was sent never goes negative.
func TestKitchenPendingQty_PerLineDelta(t *testing.T) {
	s := newHoldService()
	_, _ = s.Scan("A")
	snap := s.Snapshot()
	snap.Lines[0].KitchenSentQty = 1
	s.RestoreHeld(snap, HeldOrigin{ID: "h1"})

	if got := KitchenPendingQty(s.Lines()[0]); got != 0 {
		t.Fatalf("a fully sent line has nothing pending, got %v", got)
	}
	_, _ = s.ScanQty("A", 2) // merges into the sent line
	_, _ = s.Scan("B")       // a brand-new line
	lines := s.Lines()
	if len(lines) != 2 {
		t.Fatalf("lines = %+v", lines)
	}
	if lines[0].Qty != 3 || lines[0].KitchenSentQty != 1 {
		t.Fatalf("merged line = qty %v sent %v, want 3/1", lines[0].Qty, lines[0].KitchenSentQty)
	}
	if got := KitchenPendingQty(lines[0]); got != 2 {
		t.Fatalf("merged line pending = %v, want 2", got)
	}
	if got := KitchenPendingQty(lines[1]); got != 1 {
		t.Fatalf("new line pending = %v, want 1", got)
	}
	// Reduced after sending: nothing pending, never negative.
	if got := KitchenPendingQty(BasketLine{Qty: 1, KitchenSentQty: 3}); got != 0 {
		t.Fatalf("a line reduced below what was sent must print nothing, got %v", got)
	}
}

// Voiding every line one at a time turns the basket into a new sale (same
// rule as the held origin, ut-docs#1918): the next items must not ride on
// the old order's number.
func TestCounterOrderFields_ClearedWhenLastLineVoided(t *testing.T) {
	for _, tc := range []struct {
		name string
		void func(s *Service)
	}{
		{"remove by sku", func(s *Service) { s.Remove("A") }},
		{"remove by key", func(s *Service) { s.RemoveLine(s.Basket().Lines[0].LineKey) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newHoldService()
			_, _ = s.Scan("A")
			snap := s.Snapshot()
			snap.DisplayNo = "C-3"
			snap.Lines[0].KitchenSentQty = 1
			s.RestoreHeld(snap, HeldOrigin{ID: "h1"})
			tc.void(s)
			if s.OrderDisplayNo() != "" {
				t.Fatalf("voiding the last line must clear the order number: %q", s.OrderDisplayNo())
			}
			_, _ = s.Scan("A")
			if got := KitchenPendingQty(s.Lines()[0]); got != 1 {
				t.Fatalf("a line scanned after the order was voided must go to the kitchen, pending = %v", got)
			}
		})
	}
}
