package pos

import (
	"testing"

	"github.com/universaltill/universal-till/internal/money"
)

// ADR-0059 §3 / ut-docs#934: price-embedded scale labels must NEVER merge —
// mergeResolved's combine step sums Qty but OVERWRITES PriceCents, which for
// two differently-priced labels of one item silently drops money (€3.50 +
// €7.20 must not become qty 2 × €7.20 = €14.40).

func TestScanQty_PriceEmbeddedLinesNeverMerge(t *testing.T) {
	resolver := mapResolver{
		// Two different price labels for the SAME item.
		"P350": {SKU: "P350", Name: "Cheese", Qty: 1, PriceCents: money.FromMinor(350), ItemID: "itm1", QtyFromCode: true, NoMerge: true},
		"P720": {SKU: "P720", Name: "Cheese", Qty: 1, PriceCents: money.FromMinor(720), ItemID: "itm1", QtyFromCode: true, NoMerge: true},
	}
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000, TaxInclusive: false}, resolver)

	if _, err := s.Scan("P350"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Scan("P720"); err != nil {
		t.Fatal(err)
	}
	b := s.Basket()
	if len(b.Lines) != 2 {
		t.Fatalf("expected TWO separate lines (acceptance criterion 2), got %d: %+v", len(b.Lines), b.Lines)
	}
	if b.Lines[0].PriceCents.Minor() != 350 || b.Lines[1].PriceCents.Minor() != 720 {
		t.Fatalf("individual label prices must survive: %+v", b.Lines)
	}
	if b.Lines[0].Qty != 1 || b.Lines[1].Qty != 1 {
		t.Fatalf("price-embedded lines carry Qty 1 each, got %+v", b.Lines)
	}
	if b.Subtotal.Minor() != 1070 {
		t.Fatalf("subtotal = %d, want 1070 (350+720), never a merged wrong total", b.Subtotal.Minor())
	}
	if b.Lines[0].LineKey == "" || b.Lines[0].LineKey == b.Lines[1].LineKey {
		t.Fatalf("each appended line needs its own LineKey: %+v", b.Lines)
	}
}

// A double scan of the SAME price label yields two VISIBLE lines (the
// stated ADR trade-off: an operator can see and void the duplicate) — this
// also exercises the scan-cache path, which must preserve NoMerge.
func TestScanQty_SamePriceLabelTwiceIsTwoVisibleLines(t *testing.T) {
	resolver := mapResolver{
		"P350": {SKU: "P350", Name: "Cheese", Qty: 1, PriceCents: money.FromMinor(350), ItemID: "itm1", QtyFromCode: true, NoMerge: true},
	}
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000, TaxInclusive: false}, resolver)

	if _, err := s.Scan("P350"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Scan("P350"); err != nil { // second scan hits the scan cache
		t.Fatal(err)
	}
	b := s.Basket()
	if len(b.Lines) != 2 {
		t.Fatalf("expected two visible duplicate lines, got %d: %+v", len(b.Lines), b.Lines)
	}
	if b.Subtotal.Minor() != 700 {
		t.Fatalf("subtotal = %d, want 700", b.Subtotal.Minor())
	}
}

// A later PLAIN scan of the same item must not merge INTO an existing
// price-embedded line either — that would overwrite the label's absolute
// price with the per-unit rate.
func TestScanQty_PlainScanDoesNotMergeIntoPriceEmbeddedLine(t *testing.T) {
	resolver := mapResolver{
		"P350":   {SKU: "P350", Name: "Cheese", Qty: 1, PriceCents: money.FromMinor(350), ItemID: "itm1", QtyFromCode: true, NoMerge: true},
		"CHEESE": {SKU: "CHEESE", Name: "Cheese", Qty: 1, PriceCents: money.FromMinor(200), ItemID: "itm1"},
	}
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000, TaxInclusive: false}, resolver)

	if _, err := s.Scan("P350"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Scan("CHEESE"); err != nil {
		t.Fatal(err)
	}
	b := s.Basket()
	if len(b.Lines) != 2 {
		t.Fatalf("expected the plain scan to stay its own line, got %d: %+v", len(b.Lines), b.Lines)
	}
	if b.Lines[0].PriceCents.Minor() != 350 || b.Lines[0].Qty != 1 {
		t.Fatalf("the price-embedded line must be untouched: %+v", b.Lines[0])
	}
	if b.Lines[1].PriceCents.Minor() != 200 {
		t.Fatalf("the plain line keeps the per-unit rate: %+v", b.Lines[1])
	}
}

// Weight-embedded labels: the resolver decodes Qty from the code
// (QtyFromCode), which must win over the caller-supplied qty on BOTH the
// resolver path and the scan-cache path; two labels of the same item merge
// by summing weights at the unchanged per-unit rate (ADR-0059 §3 — the
// existing weighed-item path, no new mechanism).
func TestScanQty_WeightEmbeddedQtyComesFromCode(t *testing.T) {
	resolver := mapResolver{
		"W1234": {SKU: "W1234", Name: "Bananas", Qty: 1.234, PriceCents: money.FromMinor(200), ItemID: "itm1", IsWeighed: true, QtyFromCode: true},
	}
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000, TaxInclusive: false}, resolver)

	// The caller passes qty=1 (the scan handler's default); the label wins.
	if _, err := s.ScanQty("W1234", 1); err != nil {
		t.Fatal(err)
	}
	b := s.Basket()
	if len(b.Lines) != 1 || b.Lines[0].Qty != 1.234 {
		t.Fatalf("expected qty 1.234 decoded from the label, got %+v", b.Lines)
	}
	if b.Lines[0].PriceCents.Minor() != 200 {
		t.Fatalf("per-unit rate must be untouched: %+v", b.Lines[0])
	}

	// Rescan (cache path): weights sum, rate unchanged.
	if _, err := s.ScanQty("W1234", 1); err != nil {
		t.Fatal(err)
	}
	b = s.Basket()
	if len(b.Lines) != 1 || b.Lines[0].Qty != 2.468 {
		t.Fatalf("expected merged qty 2.468 after rescan, got %+v", b.Lines)
	}
	want := AmountForQuantity(money.FromMinor(200), 2.468)
	if b.Lines[0].LineTotal != want {
		t.Fatalf("line total = %d, want %d", b.Lines[0].LineTotal.Minor(), want.Minor())
	}
}

// A hold/resume cycle must preserve the no-merge protection: a restored
// price-embedded line must still refuse to merge with a later plain scan of
// the same item (SnapshotLine round-trips QtyFromCode/NoMerge).
func TestSnapshotRestore_PreservesNoMergeProtection(t *testing.T) {
	resolver := mapResolver{
		"P350":   {SKU: "P350", Name: "Cheese", Qty: 1, PriceCents: money.FromMinor(350), ItemID: "itm1", QtyFromCode: true, NoMerge: true},
		"CHEESE": {SKU: "CHEESE", Name: "Cheese", Qty: 1, PriceCents: money.FromMinor(200), ItemID: "itm1"},
	}
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000, TaxInclusive: false}, resolver)
	if _, err := s.Scan("P350"); err != nil {
		t.Fatal(err)
	}

	snap := s.Snapshot()
	s.Reset()
	s.Restore(snap)

	if _, err := s.Scan("CHEESE"); err != nil {
		t.Fatal(err)
	}
	b := s.Basket()
	if len(b.Lines) != 2 {
		t.Fatalf("expected the restored price line to stay separate, got %d lines: %+v", len(b.Lines), b.Lines)
	}
	if b.Lines[0].PriceCents.Minor() != 350 || b.Lines[0].Qty != 1 {
		t.Fatalf("restored price-embedded line was mutated: %+v", b.Lines[0])
	}
}
