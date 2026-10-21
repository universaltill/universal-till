package pos

import (
	"encoding/json"
	"strings"
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

// ut-docs#2703 (reopened, review): lines of a legacy counter order that
// matched nothing in the catalog ride IN the held sale's payload
// (BasketSnapshot.AddByHand), so every resume of that order -- any till,
// any number of park/resume cycles -- shows the "add by hand" notice until
// the sale is paid, cleared, or the cashier dismisses it.
func TestSnapshot_AddByHandTravelsWithTheOrder(t *testing.T) {
	s := newHoldService()
	_, _ = s.Scan("A")
	snap := s.Snapshot()
	if len(snap.AddByHand) != 0 {
		t.Fatalf("a till-rung basket has nothing to add by hand: %+v", snap.AddByHand)
	}
	snap.AddByHand = []ByHandLine{{Name: "Black Shadow", Qty: 2, Modifiers: []string{"Oat milk"}}}
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
	if got := s.Basket().AddByHand; len(got) != 1 || got[0].Name != "Black Shadow" || got[0].Qty != 2 || len(got[0].Modifiers) != 1 {
		t.Fatalf("restored basket lost the add-by-hand lines: %+v", got)
	}
	// Survives edits: scanning, and voiding every line (the cashier may be
	// about to ring the missing items up by hand).
	_, _ = s.Scan("A")
	s.Remove("A")
	if len(s.Basket().AddByHand) != 1 {
		t.Fatal("editing the basket must not drop the add-by-hand lines")
	}
	_, _ = s.Scan("A")
	// Re-park keeps them.
	if again := s.Snapshot(); len(again.AddByHand) != 1 {
		t.Fatalf("re-park snapshot dropped the add-by-hand lines: %+v", again)
	}
	// Dismissed: gone, and the next park no longer carries them.
	s.DismissAddByHand()
	if len(s.Basket().AddByHand) != 0 || len(s.Snapshot().AddByHand) != 0 {
		t.Fatal("dismiss must clear the add-by-hand lines")
	}
	// Reset (a cleared or completed sale) never leaks them into the next one.
	s.RestoreHeld(back, HeldOrigin{ID: "h1"})
	s.Reset()
	if len(s.Basket().AddByHand) != 0 {
		t.Fatal("Reset must clear the add-by-hand lines")
	}
}

// ut-docs#4206: a pay-at-counter order placed on a Shop-plan till carries the
// customer's order-tracking token in its held payload, so the sale the
// cashier later tenders can adopt it. Same lifecycle as DisplayNo: it
// survives the payload round trip and every re-park, and never leaks into
// the next, unrelated sale (Reset, or voiding the last line).
func TestSnapshot_TrackingTokenTravelsWithTheOrder(t *testing.T) {
	s := newHoldService()
	_, _ = s.Scan("A")
	snap := s.Snapshot()
	if snap.TrackingToken != "" {
		t.Fatalf("a till-rung basket carries no tracking token: %+v", snap)
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "tracking_token") {
		t.Fatalf("an untracked payload must omit tracking_token: %s", raw)
	}
	snap.TrackingToken = "0123456789abcdef0123456789abcdef"
	raw, err = json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"tracking_token":"0123456789abcdef0123456789abcdef"`) {
		t.Fatalf("payload must carry tracking_token: %s", raw)
	}
	var back BasketSnapshot
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	s.Reset()
	s.RestoreHeld(back, HeldOrigin{ID: "h1"})
	if got := s.OrderTrackingToken(); got != back.TrackingToken {
		t.Fatalf("restored basket lost the tracking token: %q", got)
	}
	if again := s.Snapshot(); again.TrackingToken != back.TrackingToken {
		t.Fatalf("re-park snapshot dropped the tracking token: %+v", again)
	}
	s.Reset()
	if s.OrderTrackingToken() != "" || s.Snapshot().TrackingToken != "" {
		t.Fatal("Reset must clear the tracking token")
	}

	for _, void := range []func(s *Service){
		func(s *Service) { s.Remove("A") },
		func(s *Service) { s.RemoveLine(s.Basket().Lines[0].LineKey) },
	} {
		s := newHoldService()
		_, _ = s.Scan("A")
		snap := s.Snapshot()
		snap.TrackingToken = "fedcba9876543210fedcba9876543210"
		s.RestoreHeld(snap, HeldOrigin{ID: "h1"})
		void(s)
		if s.OrderTrackingToken() != "" {
			t.Fatalf("voiding the last line must clear the tracking token: %q", s.OrderTrackingToken())
		}
	}
}

// ut-docs#1122: New sale records what it cleared, so the snapshot and the
// reset are one step — the returned snapshot is the basket as it was, and
// the basket is empty afterwards.
func TestResetAndSnapshot_ReturnsTheClearedBasket(t *testing.T) {
	s := newHoldService()
	_, _ = s.Scan("A")
	_, _ = s.Scan("A")
	want := s.Basket().Total
	snap := s.ResetAndSnapshot()
	if len(snap.Lines) != 1 || snap.Lines[0].Qty != 2 || snap.Total != want {
		t.Fatalf("snapshot = %+v, want one line of qty 2 totalling %v", snap, want)
	}
	if s.HasItemsOrByHand() {
		t.Fatal("ResetAndSnapshot must leave the basket empty")
	}
	if again := s.ResetAndSnapshot(); len(again.Lines) != 0 || len(again.AddByHand) != 0 {
		t.Fatalf("an empty basket snapshots empty: %+v", again)
	}
}

// ut-docs#4705: a self-order customer's language pick rides the held
// payload exactly like the tracking token: omitted when unset, kept by
// resume and re-park, cleared by Reset and by voiding the last line.
func TestSnapshot_CustomerLocaleLifecycle(t *testing.T) {
	s := newHoldService()
	_, _ = s.Scan("A")
	raw, err := json.Marshal(s.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "customer_locale") {
		t.Fatalf("a payload with no pick must omit customer_locale: %s", raw)
	}
	s.SetCustomerLocale("tr")
	raw, err = json.Marshal(s.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"customer_locale":"tr"`) {
		t.Fatalf("payload must carry customer_locale: %s", raw)
	}
	var back BasketSnapshot
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	s.Reset()
	if s.CustomerLocale() != "" {
		t.Fatal("Reset must clear the customer locale")
	}
	s.RestoreHeld(back, HeldOrigin{ID: "h1"})
	if s.CustomerLocale() != "tr" || s.Snapshot().CustomerLocale != "tr" {
		t.Fatalf("resume/re-park lost the pick: %q", s.CustomerLocale())
	}
	s.RemoveLine(s.Basket().Lines[0].LineKey)
	if s.CustomerLocale() != "" {
		t.Fatalf("voiding the last line must clear the pick: %q", s.CustomerLocale())
	}
}
