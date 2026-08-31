package pos

import (
	"reflect"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
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
//
// SCOPE (ut-docs#1351): fakeTaxAsker mocks the entire plugin chain, so this
// test only pins pos.Service's own TaxRateAsker contract — it can not catch
// a bug in internal/pages's real pluginTaxRateAsker (its generation-keyed
// cache included), the event bus, the wazero runtime, or the plugin's
// settings read. The internal/pages TestTakeawayOverride_RealChain_* tests
// (tax_takeaway_realchain_test.go) cover that whole real chain against a
// compiled wasm plugin; keep both.
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

// TestOrderTypeTaxSwitching_ModifierInheritsParentRate is ut-docs#1013's
// modifier acceptance criterion: a modifier must never introduce a tax rate
// distinct from its parent line's. Our basket model makes this true BY
// CONSTRUCTION -- data.SelectedModifier carries no rate field of its own
// (see AddLineWithModifiers), a modifier's PriceDeltaMinor is folded
// straight into the single BasketLine's PriceCents, and every rate
// decision (effectiveTaxRateBP, and the TaxRateAsker it consults) is made
// exactly once for that whole line, keyed on the LINE's own TaxCodeID.
// There is no code path where a modifier article's own rate could leak in.
//
// This test pins that invariant two ways, so it stays true on purpose:
//
//  1. A structural guard over data.SelectedModifier's own field set --
//     asserted FIRST, so a future field addition fails loudly here rather
//     than leaving the assertions below silently checking nothing.
//  2. A PRICED modifier (not 0.00): the ticket's own real trading data
//     shows a zero-price oat-milk modifier carrying a DIFFERENT rate (19%)
//     than its parent cappuccino (7% takeaway) 27 times in one day --
//     "harmless there only because the modifier was priced at 0.00...
//     wrong the moment a modifier is priced." A zero-price case alone
//     can't distinguish "inherits the parent's rate" from "the modifier
//     contributed nothing taxable at all," since a 0.00 delta changes
//     nothing about the line's tax either way -- it passes as easily with
//     AddLineWithModifiers stubbed out to ignore mods entirely. Pricing
//     the modifier at a non-zero amount that would visibly change the tax
//     under a (wrong) modifier-level-rate model is what actually exercises
//     the invariant.
func TestOrderTypeTaxSwitching_ModifierInheritsParentRate(t *testing.T) {
	modType := reflect.TypeOf(data.SelectedModifier{})
	for i := 0; i < modType.NumField(); i++ {
		if name := modType.Field(i).Name; strings.Contains(strings.ToLower(name), "rate") || strings.Contains(strings.ToLower(name), "tax") {
			t.Fatalf("data.SelectedModifier gained a rate/tax-shaped field %q -- the modifier-inherits-parent-rate invariant this test pins no longer holds by construction and needs re-deriving, not just re-passing", name)
		}
	}

	resolver := mapResolver{
		"CAPPUCCINO": {SKU: "CAPPUCCINO", ItemID: "item-cappuccino", TaxCodeID: "tax-milk-drink", Name: "Cappuccino", Qty: 1, PriceCents: 1000, TaxRateBP: 1900},
	}
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000}, resolver)
	s.SetTaxRateAsker(fakeTaxAsker{takeawayRateByTaxCode: map[string]int{"tax-milk-drink": 700}})

	base, ok := resolver.Resolve("CAPPUCCINO")
	if !ok {
		t.Fatal("resolver setup broken")
	}
	// PriceDeltaMinor: 100 (not 0) -- chosen, with PriceCents: 1000 above,
	// so the merged line total (1100) divides evenly at both 19% and 7%,
	// keeping this test's arithmetic exact rather than exercising rounding
	// as well. If oat milk carried its own 19% rate (it doesn't -- that's
	// the point), the takeaway tax below would be wrong by exactly the
	// modifier's share.
	s.AddLineWithModifiers(base, 1, []data.SelectedModifier{
		{GroupID: "milk", OptionID: "oat", GroupName: "Milk", OptionName: "Oat milk", PriceDeltaMinor: 100},
	})

	line := s.Basket().Lines[0]
	if line.TaxCodeID != "tax-milk-drink" {
		t.Fatalf("line TaxCodeID = %q, want the parent's %q", line.TaxCodeID, "tax-milk-drink")
	}
	if line.PriceCents != money.FromMinor(1100) {
		t.Fatalf("line PriceCents = %v, want 1100 (1000 base + 100 modifier delta)", line.PriceCents)
	}

	dineInTax := money.FromMinor(209) // 19% of the WHOLE 1100, modifier included -- the parent's own rate
	if got := s.Basket().Tax; got != dineInTax {
		t.Fatalf("dine-in tax with priced modifier = %v, want %v", got, dineInTax)
	}

	s.SetOrderType(OrderTypeTakeaway)
	takeawayTax := money.FromMinor(77) // 7% of the whole 1100 -- never the hypothetical 19% modifier-level rate
	if got := s.Basket().Tax; got != takeawayTax {
		t.Fatalf("takeaway tax with priced modifier = %v, want %v (never the hypothetical modifier-level rate)", got, takeawayTax)
	}
}

// TestMergeResolved_RescanUpdatesTaxCodeOnExistingLine (ut-docs#1351,
// independent finding): mergeResolved's merge-into-existing-line branch
// refreshed the surviving line's Name/PriceCents/TaxRateBP from the incoming
// scan but NOT its TaxCodeID. After a mid-sale catalog change reassigns an
// item to a different tax code (e.g. the ut-docs#512 import creating the
// paired takeaway code), a re-scan of the already-in-basket item through a
// second code (an item barcode vs. its SKU — a cache-missing path) merged
// the fresh TaxRateBP in while leaving the STALE TaxCodeID on the line — so
// the TaxRateAsker (a tax plugin keying its takeaway overrides by tax code)
// was asked about the old code and its override for the new one never
// applied.
func TestMergeResolved_RescanUpdatesTaxCodeOnExistingLine(t *testing.T) {
	resolver := mapResolver{
		// The same item reachable by two codes (SKU and barcode). Between
		// the two scans the catalog's view of the item changed tax codes —
		// modeled here by the two codes resolving to different snapshots.
		"DRINK":    {SKU: "DRINK", ItemID: "item-drink", TaxCodeID: "tax-old", Name: "Coffee", Qty: 1, PriceCents: 1000, TaxRateBP: 1900},
		"DRINK-BC": {SKU: "DRINK", ItemID: "item-drink", TaxCodeID: "tax-new", Name: "Coffee", Qty: 1, PriceCents: 1000, TaxRateBP: 1900},
	}
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000}, resolver)
	// The asker only has a takeaway opinion about the NEW tax code.
	s.SetTaxRateAsker(fakeTaxAsker{takeawayRateByTaxCode: map[string]int{"tax-new": 700}})

	if _, err := s.ScanQty("DRINK", 1); err != nil {
		t.Fatalf("ScanQty(DRINK): %v", err)
	}
	if _, err := s.ScanQty("DRINK-BC", 1); err != nil {
		t.Fatalf("ScanQty(DRINK-BC): %v", err)
	}

	lines := s.Lines()
	if len(lines) != 1 {
		t.Fatalf("expected the re-scan to merge into the existing line, got %d lines: %+v", len(lines), lines)
	}
	if lines[0].Qty != 2 {
		t.Fatalf("merged qty = %v, want 2", lines[0].Qty)
	}
	if lines[0].TaxCodeID != "tax-new" {
		t.Fatalf("merged line TaxCodeID = %q, want %q — mergeResolved refreshed TaxRateBP but left the stale tax code", lines[0].TaxCodeID, "tax-new")
	}

	// And the stale code is not just cosmetic: the effective takeaway rate
	// must follow the line's CURRENT tax code.
	s.SetOrderType(OrderTypeTakeaway)
	rate, blocked := s.EffectiveLineTaxRateBP(s.Lines()[0])
	if blocked {
		t.Fatal("unexpected blocked")
	}
	if rate != 700 {
		t.Fatalf("takeaway effective rate = %d, want 700 (the asker's override for the line's current tax code)", rate)
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
