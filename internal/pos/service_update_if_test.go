package pos

import (
	"errors"
	"testing"
)

// ut-docs#5417: UpdateLineByKeyIf is a compare-and-set on the line's qty and discount
// (ut-docs#5416 added the discount).

func seedCASLine(t *testing.T, qty float64) (*Service, string) {
	t.Helper()
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 0}, mapResolver{})
	s.AddLineWithModifiers(BasketLine{SKU: "A", ItemID: "i-a", Name: "Widget", PriceCents: 1000}, qty, nil)
	return s, s.Basket().Lines[0].LineKey
}

func TestUpdateLineByKeyIf_RefusesWhenQtyChanged(t *testing.T) {
	s, key := seedCASLine(t, 3)
	err := s.UpdateLineByKeyIf(key, 2, 0, 2, 0)
	if !errors.Is(err, ErrLineChanged) {
		t.Fatalf("want ErrLineChanged, got %v", err)
	}
	if q := s.Basket().Lines[0].Qty; q != 3 {
		t.Fatalf("line must stay at 3, got %v", q)
	}
}

func TestUpdateLineByKeyIf_RefusesMissingLine(t *testing.T) {
	s, _ := seedCASLine(t, 3)
	if err := s.UpdateLineByKeyIf("nope", 3, 0, 1, 0); !errors.Is(err, ErrLineChanged) {
		t.Fatalf("want ErrLineChanged, got %v", err)
	}
}

func TestUpdateLineByKeyIf_AppliesWhenQtyMatches(t *testing.T) {
	s, key := seedCASLine(t, 3)
	if err := s.UpdateLineByKeyIf(key, 3, 0, 5, 100); err != nil {
		t.Fatal(err)
	}
	l := s.Basket().Lines[0]
	if l.Qty != 5 || l.LineDiscount != 100 {
		t.Fatalf("got qty %v disc %v", l.Qty, l.LineDiscount)
	}
	if err := s.UpdateLineByKeyIf(key, 5, 100, 0, 0); err != nil {
		t.Fatal(err)
	}
	if n := len(s.Basket().Lines); n != 0 {
		t.Fatalf("qty 0 must remove the line, %d left", n)
	}
}

func TestUpdateLineByKeyIf_DiscountRefusalStillTyped(t *testing.T) {
	s, key := seedCASLine(t, 1)
	err := s.UpdateLineByKeyIf(key, 1, 0, 1, 999999)
	var tooLarge *LineDiscountTooLargeError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("want *LineDiscountTooLargeError, got %v", err)
	}
	if s.Basket().Lines[0].LineDiscount != 0 {
		t.Fatal("line must be unchanged")
	}
}

// ut-docs#5416: a raised discount is gated on the discount the approver saw,
// so the compare-and-set covers the discount too.
func TestUpdateLineByKeyIf_RefusesWhenDiscountChanged(t *testing.T) {
	s, key := seedCASLine(t, 3)
	if err := s.UpdateLineByKey(key, 3, 50); err != nil {
		t.Fatal(err)
	}
	err := s.UpdateLineByKeyIf(key, 3, 0, 3, 200)
	if !errors.Is(err, ErrLineChanged) {
		t.Fatalf("want ErrLineChanged, got %v", err)
	}
	if d := s.Basket().Lines[0].LineDiscount; d != 50 {
		t.Fatalf("discount must stay 50, got %v", d)
	}
}

// ut-docs#5416: New sale clears only the basket the approver saw.
func seedResetBasket(t *testing.T) *Service {
	t.Helper()
	s, _ := seedCASLine(t, 2)
	return s
}

// resetAndSnapshotNow clears the basket as it stands, via the compare-and-set.
func resetAndSnapshotNow(t *testing.T, s *Service) BasketSnapshot {
	t.Helper()
	seen := s.Snapshot()
	snap, err := s.ResetAndSnapshotIf(len(seen.Lines)+len(seen.AddByHand), seen.Total)
	if err != nil {
		t.Fatalf("ResetAndSnapshotIf: %v", err)
	}
	return snap
}

func TestResetAndSnapshotIf_RefusesWhenBasketChanged(t *testing.T) {
	s := seedResetBasket(t)
	seen := s.Snapshot()
	s.AddLineWithModifiers(BasketLine{SKU: "B", ItemID: "i-b", Name: "Gadget", PriceCents: 500}, 1, nil)
	_, err := s.ResetAndSnapshotIf(len(seen.Lines)+len(seen.AddByHand), seen.Total)
	if !errors.Is(err, ErrLineChanged) {
		t.Fatalf("want ErrLineChanged, got %v", err)
	}
	if n := len(s.Basket().Lines); n != 2 {
		t.Fatalf("nothing may be cleared, %d lines left", n)
	}
}

func TestResetAndSnapshotIf_ClearsWhenUnchanged(t *testing.T) {
	s := seedResetBasket(t)
	seen := s.Snapshot()
	got, err := s.ResetAndSnapshotIf(len(seen.Lines)+len(seen.AddByHand), seen.Total)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Lines) != 1 || got.Total != seen.Total {
		t.Fatalf("snapshot = %+v", got)
	}
	if n := len(s.Basket().Lines); n != 0 {
		t.Fatalf("basket must be empty, %d lines", n)
	}
}
