package pos

import (
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// FR/ADR-0020: a modifier's price delta is folded into the line's
// PriceCents at add-time so the rest of the money pipeline (totals, tax,
// receipts) needs no special-casing.
func TestAddLineWithModifiers_FoldsDeltaIntoPrice(t *testing.T) {
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000, TaxInclusive: false}, mapResolver{})

	base := BasketLine{SKU: "COFFEE", ItemID: "item-coffee", Name: "Flat White", PriceCents: 320}
	mods := []data.SelectedModifier{
		{GroupID: "g1", OptionID: "extra-shot", GroupName: "Extras", OptionName: "Extra shot", PriceDeltaMinor: 50},
	}
	s.AddLineWithModifiers(base, 1, mods)

	b := s.Basket()
	if len(b.Lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(b.Lines))
	}
	if b.Lines[0].PriceCents != 370 {
		t.Fatalf("expected effective price 370 (320+50), got %d", b.Lines[0].PriceCents)
	}
	if b.Subtotal != 370 {
		t.Fatalf("expected subtotal 370, got %d", b.Subtotal)
	}
	if len(b.Lines[0].Modifiers) != 1 || b.Lines[0].Modifiers[0].OptionName != "Extra shot" {
		t.Fatalf("expected modifier snapshot to be kept for display, got %+v", b.Lines[0].Modifiers)
	}
}

// Two identical customizations of the same item must merge quantity into
// one line, exactly like plain items do today.
func TestAddLineWithModifiers_IdenticalSelectionsMerge(t *testing.T) {
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000, TaxInclusive: false}, mapResolver{})
	base := BasketLine{SKU: "COFFEE", ItemID: "item-coffee", Name: "Flat White", PriceCents: 320}
	mods := []data.SelectedModifier{{OptionID: "extra-shot", OptionName: "Extra shot", PriceDeltaMinor: 50}}

	s.AddLineWithModifiers(base, 1, mods)
	s.AddLineWithModifiers(base, 1, mods)

	b := s.Basket()
	if len(b.Lines) != 1 {
		t.Fatalf("expected identical selections to merge into 1 line, got %d: %+v", len(b.Lines), b.Lines)
	}
	if b.Lines[0].Qty != 2 {
		t.Fatalf("expected merged qty 2, got %v", b.Lines[0].Qty)
	}
}

// A plain "Flat White" and a "Flat White + extra shot" price and print
// differently — they must never merge into one line even though they
// share the same SKU/ItemID.
func TestAddLineWithModifiers_DifferentSelectionsDoNotMerge(t *testing.T) {
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000, TaxInclusive: false}, mapResolver{})
	base := BasketLine{SKU: "COFFEE", ItemID: "item-coffee", Name: "Flat White", PriceCents: 320}

	s.AddLineWithModifiers(base, 1, nil)
	s.AddLineWithModifiers(base, 1, []data.SelectedModifier{{OptionID: "extra-shot", OptionName: "Extra shot", PriceDeltaMinor: 50}})

	b := s.Basket()
	if len(b.Lines) != 2 {
		t.Fatalf("expected 2 distinct lines (plain vs. customized), got %d: %+v", len(b.Lines), b.Lines)
	}
}

// Selection order must not affect the merge signature — "extra shot +
// oat milk" and "oat milk + extra shot" are the same customization.
func TestModifierSignature_OrderIndependent(t *testing.T) {
	a := BasketLine{Modifiers: []data.SelectedModifier{{OptionID: "shot"}, {OptionID: "oat"}}}
	b := BasketLine{Modifiers: []data.SelectedModifier{{OptionID: "oat"}, {OptionID: "shot"}}}
	if a.ModifierSignature() != b.ModifierSignature() {
		t.Fatalf("expected order-independent signatures to match: %q vs %q", a.ModifierSignature(), b.ModifierSignature())
	}
}

func TestModifierSignature_EmptyForNoModifiers(t *testing.T) {
	if got := (BasketLine{}).ModifierSignature(); got != "" {
		t.Fatalf("expected empty signature for a plain line, got %q", got)
	}
}

// DOCUMENTS A KNOWN GAP (see Remove's doc comment in service.go), it does
// not assert desired behavior: Remove(sku) deletes EVERY line sharing that
// SKU, which is unsafe once two modifier-distinct lines can share one SKU.
// This test exists so task #2's UI work cannot wire "remove this line" to
// Remove(sku) for a modifier-bearing item without this test forcing an
// explicit, deliberate decision (fail loudly here, not silently in
// production). If this test starts failing because Remove became
// line-specific, that's progress — update/delete this test as part of that
// fix, don't just patch it to pass again.
func TestRemove_KnownGap_DeletesAllLinesSharingSKU(t *testing.T) {
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000, TaxInclusive: false}, mapResolver{})
	base := BasketLine{SKU: "COFFEE", ItemID: "item-coffee", Name: "Flat White", PriceCents: 320}

	s.AddLineWithModifiers(base, 1, nil)
	s.AddLineWithModifiers(base, 1, []data.SelectedModifier{{OptionID: "extra-shot", OptionName: "Extra shot", PriceDeltaMinor: 50}})
	if len(s.Basket().Lines) != 2 {
		t.Fatalf("setup: expected 2 distinct lines before Remove")
	}

	s.Remove("COFFEE")

	if len(s.Basket().Lines) != 0 {
		t.Fatalf("known-gap assumption changed: expected Remove(sku) to still delete ALL same-SKU lines (got %d remaining) — if this is now line-specific, update this test and task #2's blocker note", len(s.Basket().Lines))
	}
}
