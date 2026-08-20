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

// fakeTaxAsker stands in for a real country-specific tax plugin (e.g.
// ut-plugin-tax-de) in tests — core itself has no notion of what any
// country's tax rules are, only this interface.
type fakeTaxAsker struct {
	takeawayRateByTaxCode map[string]int
}

func (f fakeTaxAsker) AskTaxRateBP(l BasketLine, orderType string) (int, bool, bool) {
	if orderType != OrderTypeTakeaway {
		return 0, false, false
	}
	bp, ok := f.takeawayRateByTaxCode[l.TaxCodeID]
	return bp, ok, false
}

// TestOrderTypeTaxSwitching verifies core has NO built-in tax-rate opinion
// about order type: with no TaxRateAsker installed, switching order type
// never changes a line's tax (today's plain default, unaffected by any
// country's rules). Installing an asker (standing in for a real tax
// plugin) is what makes it do anything at all — e.g. a drink's tax code
// switching to a reduced takeaway rate while a cake's tax code, which the
// asker declines to answer for, stays pinned to its own configured rate.
// Switching re-derives every current line immediately, including lines
// added before the order type was chosen or changed (a customer changing
// their mind mid-order, docs/germany-pos-parity-backlog.md).
func TestOrderTypeTaxSwitching(t *testing.T) {
	resolver := mapResolver{
		"DRINK": {SKU: "DRINK", ItemID: "item-drink", TaxCodeID: "tax-drink", Name: "Coffee", Qty: 1, PriceCents: 1000, TaxRateBP: 1900},
		"CAKE":  {SKU: "CAKE", ItemID: "item-cake", TaxCodeID: "tax-cake", Name: "Cake", Qty: 1, PriceCents: 1000, TaxRateBP: 700},
	}
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000}, resolver)
	for _, sku := range []string{"DRINK", "CAKE"} {
		if _, err := s.ScanQty(sku, 1); err != nil {
			t.Fatalf("ScanQty(%s) error: %v", sku, err)
		}
	}

	dineInTax := money.FromMinor(190 + 70) // 19% of 1000 + 7% of 1000
	if got := s.Basket().Tax; got != dineInTax {
		t.Fatalf("dine-in tax = %v, want %v", got, dineInTax)
	}

	// No asker installed: order type has zero effect.
	s.SetOrderType(OrderTypeTakeaway)
	if got := s.Basket().Tax; got != dineInTax {
		t.Fatalf("takeaway with no asker changed tax: got %v, want unchanged %v", got, dineInTax)
	}
	s.SetOrderType("")

	// Install the asker: drink switches to 7%, cake (unanswered) stays pinned.
	s.SetTaxRateAsker(fakeTaxAsker{takeawayRateByTaxCode: map[string]int{"tax-drink": 700}})
	if got := s.Basket().Tax; got != dineInTax {
		t.Fatalf("dine-in with asker installed changed tax: got %v, want %v", got, dineInTax)
	}

	b := *s.SetOrderType(OrderTypeTakeaway)
	if b.OrderType != OrderTypeTakeaway {
		t.Fatalf("expected order type %q, got %q", OrderTypeTakeaway, b.OrderType)
	}
	takeawayTax := money.FromMinor(70 + 70) // drink switched to 7%, cake pinned at its own 7%
	if b.Tax != takeawayTax {
		t.Fatalf("takeaway with asker: tax = %v, want %v", b.Tax, takeawayTax)
	}

	// Customer changes their mind back to dine-in mid-order.
	b = *s.SetOrderType("")
	if b.Tax != dineInTax {
		t.Fatalf("dine-in (reverted) tax = %v, want %v", b.Tax, dineInTax)
	}
}

// TestSetTableAndClearTable mirrors TestOrderTypeTaxSwitching's shape for
// the table-assignment state (ut-docs#820): SetTable stamps both the id and
// the resolved label onto the basket, and ClearTable (the "no table"
// choice, same convention as SetOrderType("")) removes both again.
func TestSetTableAndClearTable(t *testing.T) {
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000}, mapResolver{})

	if got := s.TableID(); got != "" {
		t.Fatalf("new service TableID = %q, want empty", got)
	}
	if got := s.TableLabel(); got != "" {
		t.Fatalf("new service TableLabel = %q, want empty", got)
	}

	b := *s.SetTable("tbl-1", "T1")
	if b.TableID != "tbl-1" || b.TableLabel != "T1" {
		t.Fatalf("basket after SetTable = id=%q label=%q, want tbl-1/T1", b.TableID, b.TableLabel)
	}
	if s.TableID() != "tbl-1" || s.TableLabel() != "T1" {
		t.Fatalf("accessors after SetTable = id=%q label=%q, want tbl-1/T1", s.TableID(), s.TableLabel())
	}

	// Moving to a different table overwrites both fields, not just the id.
	b = *s.SetTable("tbl-2", "T2")
	if b.TableID != "tbl-2" || b.TableLabel != "T2" {
		t.Fatalf("basket after re-SetTable = id=%q label=%q, want tbl-2/T2", b.TableID, b.TableLabel)
	}

	b = *s.ClearTable()
	if b.TableID != "" || b.TableLabel != "" {
		t.Fatalf("basket after ClearTable = id=%q label=%q, want both empty", b.TableID, b.TableLabel)
	}
	if s.TableID() != "" || s.TableLabel() != "" {
		t.Fatalf("accessors after ClearTable = id=%q label=%q, want both empty", s.TableID(), s.TableLabel())
	}
}

// TestReset_ClearsTable confirms a table assignment doesn't leak into the
// next customer's basket after Reset/Tender, the same guarantee already
// held for OrderType and the discount/customer fields.
func TestReset_ClearsTable(t *testing.T) {
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000}, mapResolver{})
	s.SetTable("tbl-1", "T1")
	s.Reset()
	if s.TableID() != "" || s.TableLabel() != "" {
		t.Fatalf("expected table cleared after Reset, got id=%q label=%q", s.TableID(), s.TableLabel())
	}
}
