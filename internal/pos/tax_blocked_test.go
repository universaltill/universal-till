package pos

// ut-docs#368: the TaxRateAsker seam must be able to say "the authority for
// this line's tax rate exists but is broken right now" — distinct from "no
// tax plugin has an opinion" — so the checkout path can fail closed on the
// affected line instead of silently ringing it up at the base rate.

import "testing"

// blockedTaxAsker stands in for the plugin-backed asker while its tax
// plugin is registered but unloadable (install_state='broken').
type blockedTaxAsker struct{}

func (blockedTaxAsker) AskTaxRateBP(l BasketLine, orderType string) (int, bool, bool) {
	return 0, false, true
}

// healthyDecliningAsker has no opinion and nothing is broken — the plain
// pre-#368 decline shape.
type healthyDecliningAsker struct{}

func (healthyDecliningAsker) AskTaxRateBP(l BasketLine, orderType string) (int, bool, bool) {
	return 0, false, false
}

func TestEffectiveLineTaxRateBP_ReportsBlocked(t *testing.T) {
	line := BasketLine{SKU: "A", Name: "Apple", Qty: 1, PriceCents: 100, TaxRateBP: 700}

	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000}, mapResolver{"A": line})
	s.SetTaxRateAsker(blockedTaxAsker{})
	rate, blocked := s.EffectiveLineTaxRateBP(line)
	if !blocked {
		t.Fatal("a blocked asker must surface blocked=true to the tender path")
	}
	// The display fallback rate is still computed (the live basket keeps
	// rendering) — blocking happens at tender, not by zeroing the preview.
	if rate != 700 {
		t.Fatalf("blocked line's display rate should stay the line's own configured rate, got %d", rate)
	}

	s.SetTaxRateAsker(healthyDecliningAsker{})
	rate, blocked = s.EffectiveLineTaxRateBP(line)
	if blocked {
		t.Fatal("a clean decline (no broken plugin) must NOT block")
	}
	if rate != 700 {
		t.Fatalf("declined line uses its own configured rate, got %d", rate)
	}

	s.SetTaxRateAsker(nil)
	rate, blocked = s.EffectiveLineTaxRateBP(line)
	if blocked {
		t.Fatal("no asker installed must never block")
	}
	if rate != 700 {
		t.Fatalf("no asker: line uses its own configured rate, got %d", rate)
	}
}

// An asker override answer wins and never blocks — a working plugin that
// answered IS the authority, whatever any other plugin's state is.
type overridingAsker struct{}

func (overridingAsker) AskTaxRateBP(l BasketLine, orderType string) (int, bool, bool) {
	return 550, true, false
}

func TestEffectiveLineTaxRateBP_OverrideWinsUnblocked(t *testing.T) {
	line := BasketLine{SKU: "A", Name: "Apple", Qty: 1, PriceCents: 100, TaxRateBP: 700}
	s := NewServiceWithResolver(Config{}, mapResolver{"A": line})
	s.SetTaxRateAsker(overridingAsker{})
	rate, blocked := s.EffectiveLineTaxRateBP(line)
	if blocked || rate != 550 {
		t.Fatalf("override answer: got (%d, blocked=%v), want (550, false)", rate, blocked)
	}
}
