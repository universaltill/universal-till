package pos

// ut-docs#3392: a shop whose configured default tax rate is genuinely 0%
// (US preset, "Other" preset, or a deliberate 0) must charge 0% on a line
// with no tax code of its own. Before the fix effectiveTaxRateBPFor treated
// a 0 default as "unconfigured" and silently charged 20%. The default rate
// reaching the sale engine is never ambiguously unset (it is always the
// UT_TAX_RATE/compiled default or a deliberately saved shop rate), so 0
// means 0.

import "testing"

func TestEffectiveLineTaxRateBP_ZeroShopDefaultChargesZero(t *testing.T) {
	none := BasketLine{SKU: "N", Name: "Mug", Qty: 1, PriceCents: 100}
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 0}, mapResolver{"N": none})

	if rate, blocked := s.EffectiveLineTaxRateBP(none); blocked || rate != 0 {
		t.Fatalf("0%% shop default, no tax code: got (%d, blocked=%v), want (0, false)", rate, blocked)
	}

	// A declining asker leaves the 0% shop default in charge too.
	s.SetTaxRateAsker(healthyDecliningAsker{})
	if rate, _ := s.EffectiveLineTaxRateBP(none); rate != 0 {
		t.Fatalf("0%% shop default with a declining asker: got %d, want 0", rate)
	}
}

func TestEffectiveLineTaxRateBP_StandardShopDefaultUnchanged(t *testing.T) {
	none := BasketLine{SKU: "N", Name: "Mug", Qty: 1, PriceCents: 100}
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000}, mapResolver{"N": none})

	if rate, blocked := s.EffectiveLineTaxRateBP(none); blocked || rate != 2000 {
		t.Fatalf("20%% shop default, no tax code: got (%d, blocked=%v), want (2000, false)", rate, blocked)
	}
}

func TestBasketTotals_ZeroShopDefaultAddsNoTax(t *testing.T) {
	for _, inclusive := range []bool{true, false} {
		none := BasketLine{SKU: "N", Name: "Mug", Qty: 1, PriceCents: 250}
		s := NewServiceWithResolver(Config{TaxRateBasisPoints: 0, TaxInclusive: inclusive}, mapResolver{"N": none})
		b, err := s.Scan("N")
		if err != nil {
			t.Fatal(err)
		}
		if !b.Tax.IsZero() {
			t.Fatalf("inclusive=%v: a line at a 0%% shop default must carry no tax, got %v", inclusive, b.Tax)
		}
		if b.Total.Minor() != 250 {
			t.Fatalf("inclusive=%v: total = %d, want 250", inclusive, b.Total.Minor())
		}
	}
}
