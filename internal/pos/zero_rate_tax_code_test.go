package pos

// ut-docs#3250: a line whose tax code is 0% (zero-rated food, an exempt
// line) must be charged 0%. Only a line with NO tax code falls back to the
// shop's default rate — before the fix both looked like TaxRateBP == 0 and
// a 0% code was silently charged the default VAT.

import "testing"

func TestEffectiveLineTaxRateBP_ZeroRateTaxCodeChargesZero(t *testing.T) {
	zero := BasketLine{SKU: "Z", Name: "Bread", Qty: 1, PriceCents: 100, TaxRateBP: 0, TaxCodeID: "tax_zero"}
	none := BasketLine{SKU: "N", Name: "Mug", Qty: 1, PriceCents: 100, TaxRateBP: 0}
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000}, mapResolver{"Z": zero, "N": none})

	if rate, blocked := s.EffectiveLineTaxRateBP(zero); blocked || rate != 0 {
		t.Fatalf("0%% tax code: got (%d, blocked=%v), want (0, false)", rate, blocked)
	}
	if rate, blocked := s.EffectiveLineTaxRateBP(none); blocked || rate != 2000 {
		t.Fatalf("no tax code: got (%d, blocked=%v), want the shop default (2000, false)", rate, blocked)
	}

	// A declining asker leaves the 0% code in charge too.
	s.SetTaxRateAsker(healthyDecliningAsker{})
	if rate, _ := s.EffectiveLineTaxRateBP(zero); rate != 0 {
		t.Fatalf("0%% tax code with a declining asker: got %d, want 0", rate)
	}
}

func TestBasketTotals_ZeroRateTaxCodeAddsNoTax(t *testing.T) {
	for _, inclusive := range []bool{true, false} {
		zero := BasketLine{SKU: "Z", Name: "Bread", Qty: 1, PriceCents: 250, TaxRateBP: 0, TaxCodeID: "tax_zero"}
		s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000, TaxInclusive: inclusive}, mapResolver{"Z": zero})
		b, err := s.Scan("Z")
		if err != nil {
			t.Fatal(err)
		}
		if !b.Tax.IsZero() {
			t.Fatalf("inclusive=%v: a 0%% tax code line must carry no tax, got %v", inclusive, b.Tax)
		}
		if b.Total.Minor() != 250 {
			t.Fatalf("inclusive=%v: total = %d, want 250", inclusive, b.Total.Minor())
		}
	}
}
