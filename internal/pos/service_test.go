package pos

import (
	"testing"

	"github.com/universaltill/universal-till/internal/money"
)

type mapResolver map[string]BasketLine

func (m mapResolver) Resolve(code string) (BasketLine, bool) {
	v, ok := m[code]
	return v, ok
}

type countingResolver struct {
	lines map[string]BasketLine
	calls int
}

func (c *countingResolver) Resolve(code string) (BasketLine, bool) {
	c.calls++
	v, ok := c.lines[code]
	return v, ok
}

func TestServiceUpdateLine(t *testing.T) {
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000, TaxInclusive: false}, mapResolver{
		"ABC": {SKU: "ABC", Name: "Apple", Qty: 1, PriceCents: 100},
	})
	if _, err := s.ScanQty("ABC", 1.5); err != nil {
		t.Fatalf("ScanQty error: %v", err)
	}
	b := s.Basket()
	if len(b.Lines) != 1 || b.Lines[0].Qty != 1.5 {
		t.Fatalf("expected qty 1.5, got %+v", b.Lines)
	}
	// update qty and discount
	s.UpdateLine("ABC", 2.0, 50)
	b = s.Basket()
	if b.Subtotal != 150 { // 2*100 -50
		t.Fatalf("expected subtotal 150, got %d", b.Subtotal)
	}
	if b.Lines[0].LineTotal != 150 {
		t.Fatalf("expected line total 150, got %d", b.Lines[0].LineTotal)
	}
	// zero qty removes line
	s.UpdateLine("ABC", 0, 0)
	b = s.Basket()
	if len(b.Lines) != 0 {
		t.Fatalf("expected line removed on zero qty")
	}
}

func TestScanQty_DuplicateScanUsesInMemoryLine(t *testing.T) {
	resolver := &countingResolver{
		lines: map[string]BasketLine{
			"ABC": {SKU: "ABC", Name: "Apple", Qty: 1, PriceCents: 100},
		},
	}
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000, TaxInclusive: false}, resolver)

	if _, err := s.ScanQty("ABC", 1); err != nil {
		t.Fatalf("ScanQty error: %v", err)
	}
	if _, err := s.ScanQty("ABC", 1); err != nil {
		t.Fatalf("ScanQty error: %v", err)
	}

	if resolver.calls != 1 {
		t.Fatalf("expected resolver to be called once (cache on duplicate), got %d", resolver.calls)
	}
	b := s.Basket()
	if len(b.Lines) != 1 || b.Lines[0].Qty != 2 {
		t.Fatalf("expected qty 2 after duplicate scan, got %+v", b.Lines)
	}
}

func TestScanQty_CacheRefreshesWeighedFlag(t *testing.T) {
	resolver := &countingResolver{
		lines: map[string]BasketLine{
			"ABC": {SKU: "ABC", Name: "Apple", Qty: 1, PriceCents: 100, ItemID: "itm1", IsWeighed: false},
			"ALT": {SKU: "ABC", Name: "Apple", Qty: 1, PriceCents: 100, ItemID: "itm1", IsWeighed: true},
		},
	}
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000, TaxInclusive: false}, resolver)

	if _, err := s.ScanQty("ABC", 1); err != nil {
		t.Fatalf("ScanQty error: %v", err)
	}
	if _, err := s.ScanQty("ALT", 1); err != nil {
		t.Fatalf("ScanQty error: %v", err)
	}

	b := s.Basket()
	if !b.Lines[0].IsWeighed {
		t.Fatalf("expected IsWeighed to refresh to true")
	}
}

func TestScanCacheClearsOnReset(t *testing.T) {
	resolver := &countingResolver{
		lines: map[string]BasketLine{
			"ABC": {SKU: "ABC", Name: "Apple", Qty: 1, PriceCents: 100},
		},
	}
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000, TaxInclusive: false}, resolver)

	if _, err := s.ScanQty("ABC", 1); err != nil {
		t.Fatalf("ScanQty error: %v", err)
	}
	s.Reset()
	resolver.lines["ABC"] = BasketLine{SKU: "ABC", Name: "Apple", Qty: 1, PriceCents: 250}

	if _, err := s.ScanQty("ABC", 1); err != nil {
		t.Fatalf("ScanQty error: %v", err)
	}
	if resolver.calls != 2 {
		t.Fatalf("expected resolver to be called after reset, got %d", resolver.calls)
	}
}

func TestScanCacheClearsOnRemove(t *testing.T) {
	resolver := &countingResolver{
		lines: map[string]BasketLine{
			"ABC": {SKU: "ABC", Name: "Apple", Qty: 1, PriceCents: 100},
		},
	}
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000, TaxInclusive: false}, resolver)

	if _, err := s.ScanQty("ABC", 1); err != nil {
		t.Fatalf("ScanQty error: %v", err)
	}
	s.Remove("ABC")
	resolver.lines["ABC"] = BasketLine{SKU: "ABC", Name: "Apple", Qty: 1, PriceCents: 250}

	if _, err := s.ScanQty("ABC", 1); err != nil {
		t.Fatalf("ScanQty error: %v", err)
	}
	if resolver.calls != 2 {
		t.Fatalf("expected resolver to be called after remove, got %d", resolver.calls)
	}
}

// TestOrderTypeTaxSwitching covers §12 UStG's dine-in/takeaway VAT switch
// (docs/germany-pos-parity-backlog.md): a line with an explicit takeaway
// rate switches, a line pinned to one rate (no takeaway override, e.g. a
// cake that's always the reduced rate) never changes, and a line with no
// per-item tax code at all falls back to the shop's global standard/reduced
// rates — all mid-basket, matching a customer changing their mind after
// items are already added.
func TestOrderTypeTaxSwitching(t *testing.T) {
	resolver := mapResolver{
		"DRINK": {SKU: "DRINK", ItemID: "item-drink", Name: "Coffee", Qty: 1, PriceCents: 1000, TaxRateBP: 1900, TakeawayRateBP: 700},
		"CAKE":  {SKU: "CAKE", ItemID: "item-cake", Name: "Cake", Qty: 1, PriceCents: 1000, TaxRateBP: 700},
		"MERCH": {SKU: "MERCH", ItemID: "item-merch", Name: "Mug", Qty: 1, PriceCents: 1000}, // no tax code — uses global rates
	}
	s := NewServiceWithResolver(Config{
		TaxRateBasisPoints:        2000, // 20% global standard
		ReducedTaxRateBasisPoints: 500,  // 5% global reduced
	}, resolver)

	for _, sku := range []string{"DRINK", "CAKE", "MERCH"} {
		if _, err := s.ScanQty(sku, 1); err != nil {
			t.Fatalf("ScanQty(%s) error: %v", sku, err)
		}
	}

	b := s.Basket()
	if b.OrderType != "" {
		t.Fatalf("expected default order type \"\", got %q", b.OrderType)
	}
	if want := money.FromMinor(460); b.Tax != want { // 190 + 70 + 200
		t.Fatalf("dine-in tax = %v, want %v", b.Tax, want)
	}

	b = *s.SetOrderType(OrderTypeTakeaway)
	if b.OrderType != OrderTypeTakeaway {
		t.Fatalf("expected order type %q, got %q", OrderTypeTakeaway, b.OrderType)
	}
	if want := money.FromMinor(190); b.Tax != want { // 70 (switched) + 70 (pinned) + 50 (global reduced)
		t.Fatalf("takeaway tax = %v, want %v", b.Tax, want)
	}

	// Customer changes their mind back to dine-in mid-order.
	b = *s.SetOrderType("")
	if want := money.FromMinor(460); b.Tax != want {
		t.Fatalf("dine-in (reverted) tax = %v, want %v", b.Tax, want)
	}
}
