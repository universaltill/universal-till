package pos

import (
	"errors"
	"testing"

	"github.com/universaltill/universal-till/internal/money"
)

// ut-docs#4627: one rule decides whether a line discount fits its line —
// the basket's /api/pos/line, the tender pre-check and computeSaleTotals
// all ask LineDiscountExceeds, so the basket can never accept a discount
// the sale write then refuses.
func TestLineDiscountExceeds(t *testing.T) {
	cases := []struct {
		name     string
		unit     int64
		qty      float64
		discount int64
		want     bool
	}{
		{"no discount", 120, 1, 0, false},
		{"equal to line total", 120, 1, 120, false},
		{"above line total", 120, 1, 150, true},
		{"fits a multi-qty line", 120, 3, 300, false},
		{"above a multi-qty line", 120, 2, 250, true},
		{"weighed line", 1000, 0.25, 251, true},
		{"negative", 120, 1, -1, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := LineDiscountExceeds(money.FromMinor(c.unit), c.qty, money.FromMinor(c.discount)); got != c.want {
				t.Fatalf("LineDiscountExceeds(%d, %v, %d) = %v, want %v", c.unit, c.qty, c.discount, got, c.want)
			}
		})
	}
}

// ut-docs#4627 review: the engine itself refuses the update under its lock
// (no read-then-write race in the handler), leaves the line unchanged and
// reports the most the discount can be.
func TestUpdateLineByKey_RefusesDiscountAboveLineTotal(t *testing.T) {
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000}, &staticResolver{lines: map[string]BasketLine{"ABC": {SKU: "ABC", Name: "Apple", Qty: 1, PriceCents: money.FromMinor(120), ItemID: "itm1", TaxRateBP: 2000}}})
	if _, err := s.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	key := s.Basket().Lines[0].LineKey
	if err := s.UpdateLineByKey(key, 3, money.FromMinor(250)); err != nil {
		t.Fatalf("a fitting discount must be accepted: %v", err)
	}
	err := s.UpdateLineByKey(key, 2, money.FromMinor(250))
	var tooLarge *LineDiscountTooLargeError
	if !errors.As(err, &tooLarge) || tooLarge.LineKey != key || tooLarge.Limit.Minor() != 240 {
		t.Fatalf("want LineDiscountTooLargeError{key, 240}, got %v", err)
	}
	if l := s.Basket().Lines[0]; l.Qty != 3 || l.LineDiscount.Minor() != 250 {
		t.Fatalf("refused update must leave the line as it was, got qty %v discount %d", l.Qty, l.LineDiscount.Minor())
	}
	if err := s.UpdateLine("ABC", 1, money.FromMinor(121)); err == nil {
		t.Fatal("UpdateLine must refuse the same way")
	}
	if err := s.UpdateLineByKey(key, 0, money.FromMinor(250)); err != nil || len(s.Basket().Lines) != 0 {
		t.Fatalf("qty 0 must still void the line, err %v lines %d", err, len(s.Basket().Lines))
	}
}
