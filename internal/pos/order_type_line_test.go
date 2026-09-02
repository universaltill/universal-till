package pos

import (
	"encoding/json"
	"testing"

	"github.com/universaltill/universal-till/internal/money"
)

// ut-docs#1181 / ADR-0073: order type is authoritative on each LINE; the
// basket's own OrderType is a derived summary ("", "takeaway" or "mixed").

func newMixedService() *Service {
	resolver := mapResolver{
		"DRINK": {SKU: "DRINK", ItemID: "item-drink", TaxCodeID: "tax-drink", Name: "Coffee", Qty: 1, PriceCents: 1000, TaxRateBP: 1900},
		"CAKE":  {SKU: "CAKE", ItemID: "item-cake", TaxCodeID: "tax-cake", Name: "Cake", Qty: 1, PriceCents: 1000, TaxRateBP: 700},
	}
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000}, resolver)
	s.SetTaxRateAsker(fakeTaxAsker{takeawayRateByTaxCode: map[string]int{"tax-drink": 700}})
	return s
}

// A new line inherits the current default; the whole-basket control still
// converts every current line (the fast common case, ADR-0073 Decision 2).
func TestLineOrderType_NewLineInheritsDefault_BulkConvertsAll(t *testing.T) {
	s := newMixedService()
	_, _ = s.Scan("DRINK")
	s.SetOrderType(OrderTypeTakeaway)
	_, _ = s.Scan("CAKE")
	b := s.Basket()
	for _, l := range b.Lines {
		if l.OrderType != OrderTypeTakeaway {
			t.Fatalf("line %s order type = %q, want takeaway after bulk switch", l.SKU, l.OrderType)
		}
	}
	if b.OrderType != OrderTypeTakeaway {
		t.Fatalf("basket summary = %q, want takeaway", b.OrderType)
	}
	if b.Tax != money.FromMinor(70+70) {
		t.Fatalf("takeaway tax = %v, want 140", b.Tax)
	}
	b = *s.SetOrderType("")
	for _, l := range b.Lines {
		if l.OrderType != "" {
			t.Fatalf("line %s order type = %q, want dine-in after bulk switch back", l.SKU, l.OrderType)
		}
	}
	if b.Tax != money.FromMinor(190+70) {
		t.Fatalf("dine-in tax = %v, want 260", b.Tax)
	}
}

// One line changed on its own is taxed for ITS mode; the basket becomes
// "mixed" and the other line is untouched.
func TestLineOrderType_PerLineChangeTaxesOnlyThatLine(t *testing.T) {
	s := newMixedService()
	_, _ = s.Scan("DRINK")
	_, _ = s.Scan("CAKE")
	drinkKey := s.Basket().Lines[0].LineKey
	b, ok := s.SetLineOrderType(drinkKey, OrderTypeTakeaway)
	if !ok {
		t.Fatal("SetLineOrderType returned !ok for a known line key")
	}
	if b.OrderType != OrderTypeMixed {
		t.Fatalf("basket summary = %q, want %q", b.OrderType, OrderTypeMixed)
	}
	if b.Lines[0].OrderType != OrderTypeTakeaway || b.Lines[1].OrderType != "" {
		t.Fatalf("line modes = %q/%q, want takeaway/dine-in", b.Lines[0].OrderType, b.Lines[1].OrderType)
	}
	// drink switched to 7%, cake stays at its own 7% dine-in rate
	if b.Tax != money.FromMinor(70+70) {
		t.Fatalf("mixed tax = %v, want 140", b.Tax)
	}
	// Gross-inclusive invariant: the customer-facing total is unchanged on
	// an inclusive catalog; here the catalog is exclusive so total moves.
	if rate, _ := s.EffectiveLineTaxRateBP(b.Lines[0]); rate != 700 {
		t.Fatalf("EffectiveLineTaxRateBP(drink takeaway) = %d, want 700", rate)
	}
	if rate, _ := s.EffectiveLineTaxRateBP(b.Lines[1]); rate != 700 {
		t.Fatalf("EffectiveLineTaxRateBP(cake dine-in) = %d, want 700", rate)
	}
	// The default for NEW lines is untouched by a per-line change.
	if s.OrderType() != "" {
		t.Fatalf("default order type = %q, want unchanged dine-in", s.OrderType())
	}
	if _, ok := s.SetLineOrderType("no-such-key", OrderTypeTakeaway); ok {
		t.Fatal("SetLineOrderType(unknown key) must report !ok")
	}
	if _, ok := s.SetLineOrderType(drinkKey, OrderTypeMixed); ok {
		t.Fatal("SetLineOrderType(mixed) must be rejected: mixed is a summary, never a line value")
	}
}

// The same product scanned once per mode must NOT merge (ADR-0073 D3).
func TestLineOrderType_SameItemDifferentModesDoNotMerge(t *testing.T) {
	s := newMixedService()
	_, _ = s.Scan("DRINK")
	s.SetOrderType(OrderTypeTakeaway)
	// bulk switch converted the first line; add a second DRINK as takeaway
	// then flip the first back to dine-in individually.
	_, _ = s.Scan("DRINK")
	b := s.Basket()
	if len(b.Lines) != 1 || b.Lines[0].Qty != 2 {
		t.Fatalf("same-mode rescan should merge: lines=%d qty=%v", len(b.Lines), b.Lines[0].Qty)
	}
	// One tap moves ONE unit of the qty-2 takeaway line to dine-in (the
	// product owner's "2 americano, one takeaway one dine in" case).
	s.SetLineOrderType(b.Lines[0].LineKey, "")
	// default is still takeaway → this scan joins the takeaway line
	_, _ = s.Scan("DRINK")
	b = s.Basket()
	if len(b.Lines) != 2 {
		t.Fatalf("cross-mode rescan merged: lines=%d, want 2", len(b.Lines))
	}
	if b.Lines[0].OrderType != OrderTypeTakeaway || b.Lines[0].Qty != 2 || b.Lines[1].OrderType != "" || b.Lines[1].Qty != 1 {
		t.Fatalf("lines = %+v, want takeaway×2 then dine-in×1", b.Lines)
	}
	// 2 × takeaway drink at 7% (140) + 1 dine-in drink at 19% (190)
	if b.Tax != money.FromMinor(140+190) {
		t.Fatalf("tax = %v, want 330", b.Tax)
	}
	if b.OrderType != OrderTypeMixed {
		t.Fatalf("summary = %q, want mixed", b.OrderType)
	}
}

// Table policy (ADR-0073 D5): a table is allowed while any dine-in line
// exists; converting the last dine-in line to takeaway clears it.
func TestLineOrderType_TablePolicyFollowsLines(t *testing.T) {
	s := newMixedService()
	_, _ = s.Scan("DRINK")
	_, _ = s.Scan("CAKE")
	s.SetTable("tbl-1", "T1")
	b := s.Basket()
	s.SetLineOrderType(b.Lines[0].LineKey, OrderTypeTakeaway)
	if got := s.TableID(); got != "tbl-1" {
		t.Fatalf("mixed basket lost its table: %q", got)
	}
	if !s.HasDineInLine() {
		t.Fatal("HasDineInLine = false for a mixed basket")
	}
	b2, _ := s.SetLineOrderType(b.Lines[1].LineKey, OrderTypeTakeaway)
	if b2.TableID != "" || s.TableID() != "" {
		t.Fatalf("all-takeaway basket kept table %q", s.TableID())
	}
	if s.HasDineInLine() {
		t.Fatal("HasDineInLine = true for an all-takeaway basket")
	}
	// SetTable on an all-takeaway basket is a no-op (existing invariant).
	s.SetTable("tbl-2", "T2")
	if s.TableID() != "" {
		t.Fatalf("SetTable assigned %q to an all-takeaway basket", s.TableID())
	}
	// A mixed basket may take a table via SetTable.
	s.SetLineOrderType(s.Basket().Lines[0].LineKey, "")
	s.SetTable("tbl-2", "T2")
	if s.TableID() != "tbl-2" {
		t.Fatalf("SetTable on a mixed basket = %q, want tbl-2", s.TableID())
	}
}

// Hold/resume keeps every line's mode; an old header-only takeaway payload
// restores as takeaway lines (ADR-0073 D6 legacy rule).
func TestLineOrderType_SnapshotRoundTripAndLegacyHeader(t *testing.T) {
	s := newMixedService()
	_, _ = s.Scan("DRINK")
	_, _ = s.Scan("CAKE")
	s.SetLineOrderType(s.Basket().Lines[0].LineKey, OrderTypeTakeaway)
	snap := s.Snapshot()
	if snap.OrderType != OrderTypeMixed {
		t.Fatalf("snapshot summary = %q, want mixed", snap.OrderType)
	}
	raw, _ := json.Marshal(snap)
	var back BasketSnapshot
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	s.Reset()
	s.Restore(back)
	b := s.Basket()
	if b.Lines[0].OrderType != OrderTypeTakeaway || b.Lines[1].OrderType != "" {
		t.Fatalf("restored modes = %q/%q", b.Lines[0].OrderType, b.Lines[1].OrderType)
	}
	if b.OrderType != OrderTypeMixed || b.Tax != money.FromMinor(70+70) {
		t.Fatalf("restored summary/tax = %q/%v", b.OrderType, b.Tax)
	}

	// Legacy payload: header takeaway, lines carry no order_type key.
	legacy := `{"lines":[{"sku":"DRINK","name":"Coffee","qty":1,"price_cents":1000,"item_id":"item-drink","tax_rate_bp":1900,"tax_code_id":"tax-drink"}],"order_type":"takeaway","total":1070}`
	var old BasketSnapshot
	if err := json.Unmarshal([]byte(legacy), &old); err != nil {
		t.Fatal(err)
	}
	s.Reset()
	s.Restore(old)
	b = s.Basket()
	if b.Lines[0].OrderType != OrderTypeTakeaway || b.OrderType != OrderTypeTakeaway {
		t.Fatalf("legacy restore: line=%q summary=%q, want takeaway/takeaway", b.Lines[0].OrderType, b.OrderType)
	}
	if s.OrderType() != OrderTypeTakeaway {
		t.Fatalf("legacy restore default = %q, want takeaway", s.OrderType())
	}
}

func TestSummarizeOrderType(t *testing.T) {
	cases := []struct {
		lines []string
		def   string
		want  string
	}{
		{nil, "", ""},
		{nil, OrderTypeTakeaway, OrderTypeTakeaway},
		{[]string{"", ""}, OrderTypeTakeaway, ""},
		{[]string{OrderTypeTakeaway}, "", OrderTypeTakeaway},
		{[]string{"", OrderTypeTakeaway}, "", OrderTypeMixed},
	}
	for _, c := range cases {
		lines := make([]BasketLine, len(c.lines))
		for i, ot := range c.lines {
			lines[i].OrderType = ot
		}
		if got := SummarizeOrderType(lines, c.def); got != c.want {
			t.Errorf("SummarizeOrderType(%v, %q) = %q, want %q", c.lines, c.def, got, c.want)
		}
	}
}

// Review H3 (ut-docs#1181): voiding the last dine-in line must clear the
// table just like converting it does — the invariant is over the LINES.
func TestLineOrderType_VoidingLastDineInLineClearsTable(t *testing.T) {
	s := newMixedService()
	_, _ = s.Scan("DRINK")
	_, _ = s.Scan("CAKE")
	s.SetTable("tbl-1", "T1")
	b := s.Basket()
	s.SetLineOrderType(b.Lines[1].LineKey, OrderTypeTakeaway) // cake to go, drink stays
	if s.TableID() != "tbl-1" {
		t.Fatal("mixed basket should keep its table")
	}
	s.RemoveLine(b.Lines[0].LineKey) // void the dine-in drink
	if got := s.TableID(); got != "" {
		t.Fatalf("all-takeaway basket after void kept table %q", got)
	}
	// Same via qty->0 and via SKU removal.
	s.Reset()
	_, _ = s.Scan("DRINK")
	s.SetOrderType(OrderTypeTakeaway)
	_, _ = s.Scan("CAKE")
	b = s.Basket()
	s.SetLineOrderType(b.Lines[1].LineKey, "")
	s.SetTable("tbl-2", "T2")
	s.UpdateLineByKey(b.Lines[1].LineKey, 0, 0)
	if got := s.TableID(); got != "" {
		t.Fatalf("all-takeaway basket after qty->0 kept table %q", got)
	}
	s.Reset()
	_, _ = s.Scan("DRINK")
	s.SetOrderType(OrderTypeTakeaway)
	_, _ = s.Scan("CAKE")
	b = s.Basket()
	s.SetLineOrderType(b.Lines[1].LineKey, "")
	s.SetTable("tbl-3", "T3")
	s.Remove("CAKE")
	if got := s.TableID(); got != "" {
		t.Fatalf("all-takeaway basket after Remove(sku) kept table %q", got)
	}
}

// Product owner, live on the tablet 2026-09-02: "suppose I have 2
// americano, one takeaway and the other one dine in" — tapping the
// per-line icon on a qty>1 line must move ONE unit to the other mode
// (splitting the line), not flip the whole line. Repeated taps move one
// unit each; the moved unit merges into an existing line of the same item
// in the target mode. A qty-1 line flips as before. Weighed/code-priced
// lines never split (a unit isn't meaningful there).
func TestSetLineOrderType_SplitsOneUnitFromMultiQtyLine(t *testing.T) {
	s := newMixedService()
	_, _ = s.ScanQty("DRINK", 2)
	b := s.Basket()
	if len(b.Lines) != 1 || b.Lines[0].Qty != 2 {
		t.Fatalf("seed: %+v", b.Lines)
	}
	key := b.Lines[0].LineKey
	b2, ok := s.SetLineOrderType(key, OrderTypeTakeaway)
	if !ok {
		t.Fatal("SetLineOrderType !ok")
	}
	if len(b2.Lines) != 2 {
		t.Fatalf("expected the line to split into 2, got %d: %+v", len(b2.Lines), b2.Lines)
	}
	if b2.Lines[0].Qty != 1 || b2.Lines[0].OrderType != "" || b2.Lines[1].Qty != 1 || b2.Lines[1].OrderType != OrderTypeTakeaway {
		t.Fatalf("after one tap: %+v", b2.Lines)
	}
	if b2.OrderType != OrderTypeMixed || b2.Tax != money.FromMinor(190+70) {
		t.Fatalf("summary/tax = %q/%v, want mixed/260", b2.OrderType, b2.Tax)
	}
	// Second tap on the remaining dine-in unit: it moves into the existing
	// takeaway line rather than creating a third line.
	b3, _ := s.SetLineOrderType(key, OrderTypeTakeaway)
	if len(b3.Lines) != 1 || b3.Lines[0].Qty != 2 || b3.Lines[0].OrderType != OrderTypeTakeaway {
		t.Fatalf("after second tap: %+v", b3.Lines)
	}
	if b3.OrderType != OrderTypeTakeaway || b3.Tax != money.FromMinor(70+70) {
		t.Fatalf("summary/tax = %q/%v, want takeaway/140", b3.OrderType, b3.Tax)
	}
	// And back: one unit returns to dine-in, splitting again.
	b4, _ := s.SetLineOrderType(b3.Lines[0].LineKey, "")
	if len(b4.Lines) != 2 || b4.OrderType != OrderTypeMixed {
		t.Fatalf("after flipping one back: %+v summary=%q", b4.Lines, b4.OrderType)
	}
	// Tapping the icon that is ALREADY the line's mode is a no-op.
	b5, _ := s.SetLineOrderType(b4.Lines[0].LineKey, b4.Lines[0].OrderType)
	if len(b5.Lines) != 2 || b5.Lines[0].Qty != b4.Lines[0].Qty {
		t.Fatalf("same-mode tap must be a no-op: %+v", b5.Lines)
	}
}

func TestSetLineOrderType_SplitProratesLineDiscount(t *testing.T) {
	s := newMixedService()
	_, _ = s.ScanQty("DRINK", 3)
	key := s.Basket().Lines[0].LineKey
	s.UpdateLineByKey(key, 3, money.FromMinor(90)) // 30 per unit
	b, _ := s.SetLineOrderType(key, OrderTypeTakeaway)
	if len(b.Lines) != 2 || b.Lines[0].Qty != 2 || b.Lines[1].Qty != 1 {
		t.Fatalf("split: %+v", b.Lines)
	}
	if b.Lines[0].LineDiscount != money.FromMinor(60) || b.Lines[1].LineDiscount != money.FromMinor(30) {
		t.Fatalf("discount not prorated: %v / %v", b.Lines[0].LineDiscount, b.Lines[1].LineDiscount)
	}
	if b.Lines[0].LineDiscount.Add(b.Lines[1].LineDiscount) != money.FromMinor(90) {
		t.Fatal("discount total changed by the split")
	}
}

func TestSetLineOrderType_WeighedLineFlipsWhole(t *testing.T) {
	resolver := mapResolver{"CHEESE": {SKU: "CHEESE", ItemID: "item-cheese", Name: "Cheese", Qty: 0.35, PriceCents: 2000, TaxRateBP: 700, IsWeighed: true}}
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000}, resolver)
	_, _ = s.ScanQty("CHEESE", 2.5)
	key := s.Basket().Lines[0].LineKey
	b, _ := s.SetLineOrderType(key, OrderTypeTakeaway)
	if len(b.Lines) != 1 || b.Lines[0].OrderType != OrderTypeTakeaway || b.Lines[0].Qty != 2.5 {
		t.Fatalf("weighed line must flip whole: %+v", b.Lines)
	}
}
