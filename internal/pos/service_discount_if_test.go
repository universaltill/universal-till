package pos

import (
	"errors"
	"testing"

	"github.com/universaltill/universal-till/internal/money"
)

// ut-docs#6858: SetDiscountIf is a compare-and-set on the computed sale discount.

func TestSetDiscountIf_RefusesWhenDiscountMoved(t *testing.T) {
	s, _ := seedCASLine(t, 3)
	s.SetDiscount(money.FromMinor(50)) // a promo lands after the caller read 0
	err := s.SetDiscountIf(0, money.FromMinor(100))
	if !errors.Is(err, ErrLineChanged) {
		t.Fatalf("want ErrLineChanged, got %v", err)
	}
	if got := s.SaleDiscount().Minor(); got != 50 {
		t.Fatalf("discount must stay 50, got %d", got)
	}
}

func TestSetDiscountIf_AppliesOnMatch(t *testing.T) {
	s, _ := seedCASLine(t, 3)
	s.SetDiscount(money.FromMinor(50))
	if err := s.SetDiscountIf(money.FromMinor(50), money.FromMinor(100)); err != nil {
		t.Fatal(err)
	}
	if got := s.SaleDiscount().Minor(); got != 100 {
		t.Fatalf("discount = %d, want 100", got)
	}
}

// A percent promo resolves to an amount; the CAS compares that computed value
// (what SaleDiscount returns), not discountValue.
func TestSetDiscountIf_PercentPromoComparesComputed(t *testing.T) {
	s, _ := seedCASLine(t, 1)  // 1 x 1000 minor
	s.SetDiscountPercent(1000) // 10% = 100
	if got := s.SaleDiscount().Minor(); got != 100 {
		t.Fatalf("computed percent discount = %d, want 100", got)
	}
	if err := s.SetDiscountIf(money.FromMinor(0), money.FromMinor(150)); !errors.Is(err, ErrLineChanged) {
		t.Fatalf("from 0 on a 100 percent promo: want ErrLineChanged, got %v", err)
	}
	if err := s.SetDiscountIf(money.FromMinor(100), money.FromMinor(150)); err != nil {
		t.Fatal(err)
	}
	if got := s.SaleDiscount().Minor(); got != 150 {
		t.Fatalf("discount = %d, want 150", got)
	}
}
