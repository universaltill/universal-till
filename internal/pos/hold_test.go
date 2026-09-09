package pos

import (
	"encoding/json"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/money"
)

type holdResolver map[string]BasketLine

func (m holdResolver) Resolve(code string) (BasketLine, bool) {
	v, ok := m[code]
	return v, ok
}

func newHoldService() *Service {
	return NewServiceWithResolver(Config{TaxInclusive: true, TaxRateBasisPoints: 2000}, holdResolver{
		"A": {SKU: "A", Name: "Apples", PriceCents: money.FromMinor(150), ItemID: "itm-a", TaxRateBP: 2000},
		"B": {SKU: "B", Name: "Bread", PriceCents: money.FromMinor(120), ItemID: "itm-b", TaxRateBP: 2000, IsWeighed: true},
	})
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	s := newHoldService()
	_, _ = s.Scan("A")
	_, _ = s.ScanQty("B", 3)
	s.SetDiscountPercent(500) // 5%
	s.SetCustomer("cust-1", "Jo")
	want := s.Basket()

	snap := s.Snapshot()
	if len(snap.Lines) != 2 {
		t.Fatalf("snapshot lines = %d, want 2", len(snap.Lines))
	}
	if snap.Total != want.Total {
		t.Fatalf("snapshot total = %v, want %v", snap.Total, want.Total)
	}

	// Survive JSON persistence like the held_sales table does.
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	var back BasketSnapshot
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}

	// Serve another customer in between.
	s.Reset()
	_, _ = s.Scan("A")
	s.Reset()

	s.RestoreHeld(back, HeldOrigin{})
	got := s.Basket()
	if len(got.Lines) != 2 {
		t.Fatalf("restored lines = %d, want 2", len(got.Lines))
	}
	if got.Total != want.Total || got.Subtotal != want.Subtotal || got.Discount != want.Discount {
		t.Fatalf("restored totals %v/%v/%v, want %v/%v/%v",
			got.Subtotal, got.Discount, got.Total, want.Subtotal, want.Discount, want.Total)
	}
	if got.CustomerName != "Jo" || got.CustomerID != "cust-1" {
		t.Fatalf("restored customer = %q/%q", got.CustomerID, got.CustomerName)
	}
	// Hidden fields must survive (they never hit the basket wire format).
	if !s.HasItems() {
		t.Fatal("expected items after restore")
	}
	lines := s.Lines()
	for _, l := range lines {
		if l.SKU == "B" && (!l.IsWeighed || l.ItemID != "itm-b" || l.TaxRateBP != 2000) {
			t.Fatalf("hidden fields lost on line B: %+v", l)
		}
	}
}

// TestSnapshotRestoreRoundTrip_PreservesTable (ut-docs#820): a held/resumed
// order must not lose its assigned table, the same guarantee
// TestSnapshotRestoreRoundTrip already holds for the customer/discount
// fields.
func TestSnapshotRestoreRoundTrip_PreservesTable(t *testing.T) {
	s := newHoldService()
	_, _ = s.Scan("A")
	s.SetTable("tbl-1", "T1")

	snap := s.Snapshot()
	if snap.TableID != "tbl-1" || snap.TableLabel != "T1" {
		t.Fatalf("snapshot table = id=%q label=%q, want tbl-1/T1", snap.TableID, snap.TableLabel)
	}

	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	var back BasketSnapshot
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}

	// Serve another customer in between, at a different table.
	s.Reset()
	_, _ = s.Scan("A")
	s.SetTable("tbl-9", "T9")
	s.Reset()

	s.RestoreHeld(back, HeldOrigin{})
	if got := s.TableID(); got != "tbl-1" {
		t.Fatalf("restored TableID = %q, want tbl-1", got)
	}
	if got := s.TableLabel(); got != "T1" {
		t.Fatalf("restored TableLabel = %q, want T1", got)
	}
	if got := s.Basket().TableID; got != "tbl-1" {
		t.Fatalf("restored basket TableID = %q, want tbl-1", got)
	}
}

// TestSnapshotRestoreRoundTrip_PreservesOrderType (ut-docs#1381): a held
// takeaway order must resume as takeaway, not silently revert to dine-in —
// Restore's resetLocked() call used to zero orderType back to "" (dine-in)
// before ever restoring it, changing the sale's VAT basis on every resume
// (§12 UStG: EffectiveLineTaxRateBP/recomputeTotals both key off orderType).
// Uses fakeTaxAsker (service_test.go, same package) so the takeaway/dine-in
// rates genuinely differ — a same-rate round trip couldn't have caught this.
func TestSnapshotRestoreRoundTrip_PreservesOrderType(t *testing.T) {
	resolver := mapResolver{
		"DRINK": {SKU: "DRINK", ItemID: "item-drink", TaxCodeID: "tax-drink", Name: "Coffee", PriceCents: money.FromMinor(1000), TaxRateBP: 1900},
	}
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000}, resolver)
	s.SetTaxRateAsker(fakeTaxAsker{takeawayRateByTaxCode: map[string]int{"tax-drink": 700}})
	if _, err := s.Scan("DRINK"); err != nil {
		t.Fatalf("Scan error: %v", err)
	}
	s.SetOrderType(OrderTypeTakeaway)

	if rateBP, blocked := s.EffectiveLineTaxRateBP(s.Lines()[0]); blocked || rateBP != 700 {
		t.Fatalf("pre-hold takeaway rate = %d (blocked=%v), want 700", rateBP, blocked)
	}

	snap := s.Snapshot()
	if snap.OrderType != OrderTypeTakeaway {
		t.Fatalf("snapshot order type = %q, want %q", snap.OrderType, OrderTypeTakeaway)
	}

	// Survive JSON persistence like the held_sales table does.
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	var back BasketSnapshot
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}

	// Serve another (dine-in) customer in between.
	s.Reset()
	_, _ = s.Scan("DRINK")
	s.Reset()

	s.RestoreHeld(back, HeldOrigin{})
	if got := s.OrderType(); got != OrderTypeTakeaway {
		t.Fatalf("restored OrderType() = %q, want %q (silently reverted to dine-in)", got, OrderTypeTakeaway)
	}
	if got := s.Basket().OrderType; got != OrderTypeTakeaway {
		t.Fatalf("restored basket.OrderType = %q, want %q", got, OrderTypeTakeaway)
	}
	// Independent review: pin the actual money, not just the rate --
	// this is a VAT-basis compliance ticket, so the number that ends up
	// on the receipt/sale record is what actually matters. 7% of a
	// £10.00 (tax-exclusive) line is £0.70; the pre-fix bug would have
	// this at 19% (£1.90, dine-in's rate).
	if got := s.Basket().Tax; got != money.FromMinor(70) {
		t.Fatalf("restored basket.Tax = %v, want 70 (7%% takeaway rate, not dine-in's 19%%)", got)
	}
	if rateBP, blocked := s.EffectiveLineTaxRateBP(s.Lines()[0]); blocked || rateBP != 700 {
		t.Fatalf("restored effective rate = %d (blocked=%v), want 700 (still takeaway, not dine-in's 1900)", rateBP, blocked)
	}
}

func TestHasItems(t *testing.T) {
	s := newHoldService()
	if s.HasItems() {
		t.Fatal("new service should be empty")
	}
	_, _ = s.Scan("A")
	if !s.HasItems() {
		t.Fatal("expected items after scan")
	}
	s.Reset()
	if s.HasItems() {
		t.Fatal("expected empty after reset")
	}
}

// ADR-0020: a held sale must not silently drop a customer's chosen
// modifiers on recall — the whole point of hold/resume is serving another
// customer without losing what's already in progress.
func TestSnapshotRestoreRoundTrip_PreservesModifiers(t *testing.T) {
	s := newHoldService()
	base := BasketLine{SKU: "A", Name: "Apples", PriceCents: money.FromMinor(150), ItemID: "itm-a", TaxRateBP: 2000}
	mods := []data.SelectedModifier{{GroupID: "g1", OptionID: "opt1", GroupName: "Size", OptionName: "Large", PriceDeltaMinor: 30}}
	s.AddLineWithModifiers(base, 1, mods)
	want := s.Basket()

	snap := s.Snapshot()
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	var back BasketSnapshot
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}

	s.Reset()
	s.RestoreHeld(back, HeldOrigin{})
	got := s.Basket()

	if got.Total != want.Total {
		t.Fatalf("restored total %v, want %v (modifier price delta lost?)", got.Total, want.Total)
	}
	if len(got.Lines) != 1 || len(got.Lines[0].Modifiers) != 1 {
		t.Fatalf("expected 1 line with 1 modifier restored, got %+v", got.Lines)
	}
	if got.Lines[0].Modifiers[0].OptionName != "Large" || got.Lines[0].Modifiers[0].PriceDeltaMinor != 30 {
		t.Fatalf("modifier snapshot corrupted on restore: %+v", got.Lines[0].Modifiers[0])
	}
}

// TestRestoreHeld_RecordsOriginUntilResetOrTender (ut-docs#1918): the
// held-sale origin is the order's stable identity across park -> resume ->
// re-park. It must be readable after RestoreHeld, absent after a plain
// Restore (a snapshot with no row behind it), and cleared by every path
// that ends the basket's life -- Reset, a fresh RestoreHeld/Restore, and
// Tender -- so the next customer's sale can never inherit it.
func TestRestoreHeld_RecordsOriginUntilResetOrTender(t *testing.T) {
	s := newHoldService()
	if got := s.HeldOrigin(); !got.IsZero() {
		t.Fatalf("new service HeldOrigin = %+v, want zero", got)
	}
	_, _ = s.Scan("A")
	snap := s.Snapshot()
	s.Reset()

	origin := HeldOrigin{ID: "hold-1", Label: "Table 4", CreatedAt: "2026-09-09 10:00:00"}
	s.RestoreHeld(snap, origin)
	if got := s.HeldOrigin(); got != origin {
		t.Fatalf("HeldOrigin after RestoreHeld = %+v, want %+v", got, origin)
	}
	if len(s.Basket().Lines) != 1 {
		t.Fatalf("RestoreHeld must still restore the lines, got %d", len(s.Basket().Lines))
	}

	// A plain Restore (no row behind it) wipes any previous origin.
	s.RestoreHeld(snap, HeldOrigin{})
	if got := s.HeldOrigin(); !got.IsZero() {
		t.Fatalf("HeldOrigin after plain Restore = %+v, want zero", got)
	}

	s.RestoreHeld(snap, origin)
	s.Reset()
	if got := s.HeldOrigin(); !got.IsZero() {
		t.Fatalf("HeldOrigin after Reset = %+v, want zero", got)
	}

	s.RestoreHeld(snap, origin)
	if _, err := s.Tender(money.FromMinor(150), "cash"); err != nil {
		t.Fatal(err)
	}
	if got := s.HeldOrigin(); !got.IsZero() {
		t.Fatalf("HeldOrigin after Tender = %+v, want zero", got)
	}
}

// TestVoidingEveryLine_ClearsHeldOrigin (ut-docs#1918, independent review
// finding): a cashier can also empty a resumed basket one line at a time
// (RemoveLine/Remove) instead of tapping Reset. Without this, heldOrigin
// keeps pointing at the old held_sales row after every line is gone, and an
// UNRELATED sale rung up in the same "session" and later parked would be
// upserted under the old order's id/label/created_at -- silently replacing
// it on the Open orders page.
func TestVoidingEveryLine_ClearsHeldOrigin(t *testing.T) {
	s := newHoldService()
	_, _ = s.Scan("A")
	_, _ = s.Scan("B")
	snap := s.Snapshot()
	s.Reset()
	origin := HeldOrigin{ID: "hold-1", Label: "Table 4", CreatedAt: "2026-09-09 10:00:00"}
	s.RestoreHeld(snap, origin)

	lines := s.Basket().Lines
	if len(lines) != 2 {
		t.Fatalf("setup: want 2 lines, got %d", len(lines))
	}
	// Void one of two lines: origin must survive, this is still the SAME
	// order, just missing an item.
	s.RemoveLine(lines[0].LineKey)
	if got := s.HeldOrigin(); got != origin {
		t.Fatalf("HeldOrigin after voiding one of two lines = %+v, want unchanged %+v", got, origin)
	}
	// Void the last line: the basket is now empty, and nothing describes
	// this order any more -- origin must clear.
	s.RemoveLine(lines[1].LineKey)
	if s.HasItems() {
		t.Fatalf("setup: basket should be empty after voiding both lines")
	}
	if got := s.HeldOrigin(); !got.IsZero() {
		t.Fatalf("HeldOrigin after voiding every line = %+v, want zero", got)
	}

	// Same guarantee via the SKU-based Remove (merged-line path).
	_, _ = s.Scan("A")
	_, _ = s.Scan("A")
	snap2 := s.Snapshot()
	s.Reset()
	s.RestoreHeld(snap2, origin)
	s.Remove("A")
	if got := s.HeldOrigin(); !got.IsZero() {
		t.Fatalf("HeldOrigin after Remove(sku) empties the basket = %+v, want zero", got)
	}
}
