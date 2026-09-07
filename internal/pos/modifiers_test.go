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

// mergeResolved's per-line modifier signature is memoized (ut-docs#1359).
// Restore rebuilds s.lines directly from a BasketSnapshot, bypassing
// mergeResolved entirely, so a restored line never had its cache populated
// before this test's adds run. If the cache were ever wrongly pre-seeded
// or defaulted to something other than "not yet computed," a subsequent
// add could merge into the wrong sibling line — a real money/tax bug
// (silently changing a customized line's modifiers or price). This proves
// a fresh-after-restore line still gets its signature computed correctly,
// not from stale/garbage cache state.
func TestRestore_ThenAddLineWithModifiers_MergesIntoCorrectSibling(t *testing.T) {
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000, TaxInclusive: false}, mapResolver{})
	base := BasketLine{SKU: "COFFEE", ItemID: "item-coffee", Name: "Flat White", PriceCents: 320}
	shotMods := []data.SelectedModifier{{OptionID: "extra-shot", OptionName: "Extra shot", PriceDeltaMinor: 50}}

	// Build a plain line and a customized line, hold, then restore into a
	// brand-new Service — this is the path that produces cache-less lines.
	s.AddLineWithModifiers(base, 1, nil)
	s.AddLineWithModifiers(base, 1, shotMods)
	if len(s.Basket().Lines) != 2 {
		t.Fatalf("setup: expected 2 distinct lines before hold, got %d", len(s.Basket().Lines))
	}
	snap := s.Snapshot()

	restored := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000, TaxInclusive: false}, mapResolver{})
	restored.Restore(snap)
	if len(restored.Basket().Lines) != 2 {
		t.Fatalf("setup: expected 2 distinct lines after restore, got %d", len(restored.Basket().Lines))
	}

	// Add one more plain coffee and one more customized coffee. Each must
	// merge into its OWN matching sibling, never the other.
	restored.AddLineWithModifiers(base, 1, nil)
	restored.AddLineWithModifiers(base, 1, shotMods)

	lines := restored.Basket().Lines
	if len(lines) != 2 {
		t.Fatalf("expected merges to keep exactly 2 distinct lines, got %d: %+v", len(lines), lines)
	}
	for _, l := range lines {
		if len(l.Modifiers) == 0 {
			if l.Qty != 2 {
				t.Fatalf("plain line: expected qty 2 after merge, got %v", l.Qty)
			}
		} else {
			if l.Qty != 2 {
				t.Fatalf("customized line: expected qty 2 after merge, got %v", l.Qty)
			}
			if l.Modifiers[0].OptionID != "extra-shot" {
				t.Fatalf("customized line: expected its own modifier to survive the merge, got %+v", l.Modifiers)
			}
		}
	}
}

// Independent-review regression (ut-docs#1359): the memoized modifier
// signature must never outlive the modifier selection it describes.
//
// Basket() and Lines() hand out BasketLine VALUES whose signature memo is
// already populated (mergeResolved fills it in on every line it scans). A
// caller that re-uses one of those handed-out lines as the `base` of a new
// AddLineWithModifiers call — an entirely ordinary use of the exported API,
// e.g. "ring the same drink up again, plain" — would carry the OLD
// selection's cached signature onto a line whose Modifiers are being
// replaced. mergeResolved would then compare the wrong key and merge the
// plain add into the customized line, silently dropping the customization
// and its folded-in price delta: wrong price, wrong receipt, wrong tax
// basis. Confirmed as a real regression: this test passes against
// pre-cache HEAD and failed against the first cut of the cache.
func TestAddLineWithModifiers_BaseFromBasketDoesNotReuseStaleSignature(t *testing.T) {
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000, TaxInclusive: false}, mapResolver{})
	base := BasketLine{SKU: "COFFEE", ItemID: "item-coffee", Name: "Flat White", PriceCents: 320}
	shot := []data.SelectedModifier{
		{GroupID: "g1", OptionID: "extra-shot", GroupName: "Extras", OptionName: "Extra shot", PriceDeltaMinor: 50},
	}
	s.AddLineWithModifiers(base, 1, shot)

	// Take the line straight back out of the exported API and re-use it as
	// the base for a PLAIN add of the same drink.
	reused := s.Basket().Lines[0]
	reused.LineKey = ""
	reused.PriceCents = 320 // the plain, pre-modifier price
	s.AddLineWithModifiers(reused, 1, nil)

	lines := s.Basket().Lines
	if len(lines) != 2 {
		t.Fatalf("stale signature: plain add merged into the customized line — want 2 distinct lines, got %d: %+v", len(lines), lines)
	}
	var plain, customized *BasketLine
	for i := range lines {
		if len(lines[i].Modifiers) == 0 {
			plain = &lines[i]
		} else {
			customized = &lines[i]
		}
	}
	if plain == nil || customized == nil {
		t.Fatalf("want one plain and one customized line, got %+v", lines)
	}
	if customized.Qty != 1 || customized.PriceCents != 370 {
		t.Fatalf("customized line must be untouched by the plain add: qty=%v price=%v", customized.Qty, customized.PriceCents)
	}
	if customized.Modifiers[0].OptionID != "extra-shot" {
		t.Fatalf("customized line lost its modifier: %+v", customized.Modifiers)
	}
	if plain.Qty != 1 || plain.PriceCents != 320 {
		t.Fatalf("plain line wrong: qty=%v price=%v", plain.Qty, plain.PriceCents)
	}
	if got := s.Basket().Subtotal; got != 690 { // 370 customized + 320 plain
		t.Fatalf("want subtotal 690, got %v", got)
	}
}

// The same trap one level down: setModifiers is the only sanctioned way to
// replace an existing line's Modifiers. Asserted directly so a future edit
// that reintroduces a bare `l.Modifiers = ...` on an already-cached line is
// caught here even if no Service-level path happens to exercise it yet.
func TestSetModifiers_InvalidatesSignatureMemo(t *testing.T) {
	l := BasketLine{SKU: "COFFEE", Modifiers: []data.SelectedModifier{{OptionID: "extra-shot"}}}
	if got := l.cachedModifierSignature(); got != "extra-shot" {
		t.Fatalf("want %q, got %q", "extra-shot", got)
	}
	l.setModifiers([]data.SelectedModifier{{OptionID: "oat-milk"}})
	if got := l.cachedModifierSignature(); got != "oat-milk" {
		t.Fatalf("memo not invalidated: want %q, got %q", "oat-milk", got)
	}
	l.setModifiers(nil)
	if got := l.cachedModifierSignature(); got != "" {
		t.Fatalf("memo not invalidated on clear: want %q, got %q", "", got)
	}
}
