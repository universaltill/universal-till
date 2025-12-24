package pos

import "testing"

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
