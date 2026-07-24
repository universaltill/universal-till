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

// Remove(sku) is the LEGACY, SKU-keyed method — kept for callers that only
// ever have one line per SKU. It still deletes every line sharing that SKU
// (documented on its doc comment), which is exactly why the cashier UI uses
// RemoveLine (by LineKey) instead — see the next two tests.
func TestRemove_LegacySKUMethod_DeletesAllLinesSharingSKU(t *testing.T) {
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000, TaxInclusive: false}, mapResolver{})
	base := BasketLine{SKU: "COFFEE", ItemID: "item-coffee", Name: "Flat White", PriceCents: 320}

	s.AddLineWithModifiers(base, 1, nil)
	s.AddLineWithModifiers(base, 1, []data.SelectedModifier{{OptionID: "extra-shot", OptionName: "Extra shot", PriceDeltaMinor: 50}})
	if len(s.Basket().Lines) != 2 {
		t.Fatalf("setup: expected 2 distinct lines before Remove")
	}

	s.Remove("COFFEE")

	if len(s.Basket().Lines) != 0 {
		t.Fatalf("expected legacy Remove(sku) to delete ALL same-SKU lines, got %d remaining", len(s.Basket().Lines))
	}
}

// RemoveLine (by LineKey, ADR-0020) is what the cashier UI actually uses —
// it must remove exactly the targeted line and leave a same-SKU sibling
// (different customization) untouched.
func TestRemoveLine_TargetsExactlyOneLineEvenWhenSKUsCollide(t *testing.T) {
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000, TaxInclusive: false}, mapResolver{})
	base := BasketLine{SKU: "COFFEE", ItemID: "item-coffee", Name: "Flat White", PriceCents: 320}

	s.AddLineWithModifiers(base, 1, nil)
	s.AddLineWithModifiers(base, 1, []data.SelectedModifier{{OptionID: "extra-shot", OptionName: "Extra shot", PriceDeltaMinor: 50}})
	lines := s.Basket().Lines
	if len(lines) != 2 {
		t.Fatalf("setup: expected 2 distinct lines, got %d", len(lines))
	}
	var plainKey string
	for _, l := range lines {
		if len(l.Modifiers) == 0 {
			plainKey = l.LineKey
		}
	}
	if plainKey == "" {
		t.Fatalf("setup: could not find the plain line's key: %+v", lines)
	}

	s.RemoveLine(plainKey)

	got := s.Basket().Lines
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 line left, got %d: %+v", len(got), got)
	}
	if len(got[0].Modifiers) == 0 {
		t.Fatalf("removed the wrong line — the customized one should have survived: %+v", got[0])
	}
}

// UpdateLineByKey (ADR-0020) must adjust only the targeted line's qty, not
// a same-SKU sibling with different modifiers.
func TestUpdateLineByKey_TargetsExactlyOneLineEvenWhenSKUsCollide(t *testing.T) {
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000, TaxInclusive: false}, mapResolver{})
	base := BasketLine{SKU: "COFFEE", ItemID: "item-coffee", Name: "Flat White", PriceCents: 320}

	s.AddLineWithModifiers(base, 1, nil)
	s.AddLineWithModifiers(base, 1, []data.SelectedModifier{{OptionID: "extra-shot", OptionName: "Extra shot", PriceDeltaMinor: 50}})
	var plainKey, customKey string
	for _, l := range s.Basket().Lines {
		if len(l.Modifiers) == 0 {
			plainKey = l.LineKey
		} else {
			customKey = l.LineKey
		}
	}

	s.UpdateLineByKey(plainKey, 5, 0)

	for _, l := range s.Basket().Lines {
		switch l.LineKey {
		case plainKey:
			if l.Qty != 5 {
				t.Fatalf("targeted line qty = %v, want 5", l.Qty)
			}
		case customKey:
			if l.Qty != 1 {
				t.Fatalf("untargeted sibling line qty changed to %v, want unchanged 1", l.Qty)
			}
		}
	}
}
