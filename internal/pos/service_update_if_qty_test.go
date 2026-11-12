package pos

import (
	"errors"
	"testing"
)

// ut-docs#5417: UpdateLineByKeyIfQty is a compare-and-set on the line's qty.

func seedCASLine(t *testing.T, qty float64) (*Service, string) {
	t.Helper()
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 0}, mapResolver{})
	s.AddLineWithModifiers(BasketLine{SKU: "A", ItemID: "i-a", Name: "Widget", PriceCents: 1000}, qty, nil)
	return s, s.Basket().Lines[0].LineKey
}

func TestUpdateLineByKeyIfQty_RefusesWhenQtyChanged(t *testing.T) {
	s, key := seedCASLine(t, 3)
	err := s.UpdateLineByKeyIfQty(key, 2, 2, 0)
	if !errors.Is(err, ErrLineChanged) {
		t.Fatalf("want ErrLineChanged, got %v", err)
	}
	if q := s.Basket().Lines[0].Qty; q != 3 {
		t.Fatalf("line must stay at 3, got %v", q)
	}
}

func TestUpdateLineByKeyIfQty_RefusesMissingLine(t *testing.T) {
	s, _ := seedCASLine(t, 3)
	if err := s.UpdateLineByKeyIfQty("nope", 3, 1, 0); !errors.Is(err, ErrLineChanged) {
		t.Fatalf("want ErrLineChanged, got %v", err)
	}
}

func TestUpdateLineByKeyIfQty_AppliesWhenQtyMatches(t *testing.T) {
	s, key := seedCASLine(t, 3)
	if err := s.UpdateLineByKeyIfQty(key, 3, 5, 100); err != nil {
		t.Fatal(err)
	}
	l := s.Basket().Lines[0]
	if l.Qty != 5 || l.LineDiscount != 100 {
		t.Fatalf("got qty %v disc %v", l.Qty, l.LineDiscount)
	}
	if err := s.UpdateLineByKeyIfQty(key, 5, 0, 0); err != nil {
		t.Fatal(err)
	}
	if n := len(s.Basket().Lines); n != 0 {
		t.Fatalf("qty 0 must remove the line, %d left", n)
	}
}

func TestUpdateLineByKeyIfQty_DiscountRefusalStillTyped(t *testing.T) {
	s, key := seedCASLine(t, 1)
	err := s.UpdateLineByKeyIfQty(key, 1, 1, 999999)
	var tooLarge *LineDiscountTooLargeError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("want *LineDiscountTooLargeError, got %v", err)
	}
	if s.Basket().Lines[0].LineDiscount != 0 {
		t.Fatal("line must be unchanged")
	}
}
