package pos

import (
	"testing"

	"github.com/universaltill/universal-till/internal/money"
)

// This file exercises ut-docs#1014's invariant: for a gross-priced
// (tax-inclusive) catalog, changing consumption mode / tax rate must never
// change the customer-facing (gross) price — only the embedded net/tax
// split moves. See ComputeTaxBasisPoints's doc comment (money.go) and
// SetOrderType's doc comment (service.go) for the statement of the rule
// these tests pin down.

// TestComputeTaxBasisPoints_InclusiveGrossInvariant_RateMatrix asserts the
// invariant at the level of the shared primitive itself, across the full
// rate matrix a real German till sees (including the exact Ausser
// Haus/Im Haus pair from the card's real trading data) plus edge rates.
func TestComputeTaxBasisPoints_InclusiveGrossInvariant_RateMatrix(t *testing.T) {
	grosses := []money.Money{
		money.FromMinor(480), // 4.80 — the card's own cappuccino example
		money.FromMinor(100),
		money.FromMinor(999),
		money.FromMinor(10000),
		money.FromMinor(1), // smallest possible unit
	}
	rates := []int{0, 700, 1000, 1900, 2000, 750, 1925} // incl. 7%/19% DE pair
	for _, gross := range grosses {
		for _, rateBP := range rates {
			tax, total := ComputeTaxBasisPoints(gross, rateBP, true)
			if total != gross {
				t.Fatalf("inclusive gross=%v rateBP=%d: total=%v, want unchanged gross %v", gross, rateBP, total, gross)
			}
			// Independently recompute the expected tax the same way a
			// correct implementation must (net = gross/(1+rate), half-up;
			// tax = gross's complement of that net) — asserting against a
			// value derived the same way ComputeTaxBasisPoints itself is
			// documented to derive it, not against a tautological identity
			// that would hold for any tax value.
			wantNet := gross.MulDiv(basisPointsDen, basisPointsDen+int64(rateBP))
			wantTax := gross.Sub(wantNet)
			if tax != wantTax {
				t.Fatalf("inclusive gross=%v rateBP=%d: tax=%v, want %v", gross, rateBP, tax, wantTax)
			}
			if tax.IsNegative() || wantNet.IsNegative() {
				t.Fatalf("inclusive gross=%v rateBP=%d: negative net/tax (net=%v tax=%v)", gross, rateBP, wantNet, tax)
			}
		}
	}
}

// TestComputeTaxBasisPoints_InclusiveGrossInvariant_DESwitch is the exact
// scenario from the card: the SAME gross price (4.80) sold once at 19%
// (Im Haus) and once at 7% (Ausser Haus) — only the net/tax split may move.
// The tax figures are pinned to the card's own real trading numbers (net
// 4.033613 @19% / net 4.485981 @7%, both rounding to gross 4.80) so a bug
// that computes `tax` independently of `subtotal.Sub(net)` (which would
// still pass a gross-unchanged-only check, see ComputeTaxBasisPoints's doc
// comment) is caught here too.
func TestComputeTaxBasisPoints_InclusiveGrossInvariant_DESwitch(t *testing.T) {
	gross := money.FromMinor(480)

	imHausTax, imHausTotal := ComputeTaxBasisPoints(gross, 1900, true)
	ausserHausTax, ausserHausTotal := ComputeTaxBasisPoints(gross, 700, true)

	if imHausTotal != gross || ausserHausTotal != gross {
		t.Fatalf("gross moved across the switch: imHaus=%v ausserHaus=%v, want both %v", imHausTotal, ausserHausTotal, gross)
	}
	// net 4.033613 -> rounds to 4.03 -> tax = 4.80-4.03 = 0.77
	if want := money.FromMinor(77); imHausTax != want {
		t.Fatalf("Im Haus (19%%) tax = %v, want %v", imHausTax, want)
	}
	// net 4.485981 -> rounds to 4.49 -> tax = 4.80-4.49 = 0.31
	if want := money.FromMinor(31); ausserHausTax != want {
		t.Fatalf("Ausser Haus (7%%) tax = %v, want %v", ausserHausTax, want)
	}
}

// TestComputeTaxBasisPoints_ExclusiveNetInvariant_Contrast guards the
// mirror-image case: for a NET-priced (tax-exclusive) catalog, it is the
// NET that must stay fixed across a rate change, and the GROSS that is free
// to move. A future change that "fixes" this to also hold gross fixed would
// silently break net-priced catalogs, so this stays a separate, explicit
// assertion rather than relying on the inclusive tests alone.
func TestComputeTaxBasisPoints_ExclusiveNetInvariant_Contrast(t *testing.T) {
	net := money.FromMinor(480)

	_, totalAt19 := ComputeTaxBasisPoints(net, 1900, false)
	_, totalAt7 := ComputeTaxBasisPoints(net, 700, false)

	if totalAt19 == totalAt7 {
		t.Fatalf("exclusive mode: gross did not move with the rate (both %v) — exclusive must stay distinct from inclusive", totalAt19)
	}
	if totalAt19 <= totalAt7 {
		t.Fatalf("exclusive mode: higher rate (19%%) should produce a higher gross than 7%%, got %v <= %v", totalAt19, totalAt7)
	}
}

// TestGrossInclusiveInvariant_OrderTypeSwitch_German drives the invariant
// through the full Service/basket path (not just the primitive): a
// tax-inclusive till with a drink taxed 19% dine-in / 7% takeaway, switched
// via SetOrderType exactly as the sale-screen toggle does. Basket.Total
// (what the customer owes) must not move; Basket.Tax must.
func TestGrossInclusiveInvariant_OrderTypeSwitch_German(t *testing.T) {
	resolver := mapResolver{
		"CAPP": {SKU: "CAPP", ItemID: "item-capp", TaxCodeID: "tax-drink", Name: "Cappuccino", Qty: 1, PriceCents: money.FromMinor(480), TaxRateBP: 1900},
	}
	s := NewServiceWithResolver(Config{TaxInclusive: true, TaxRateBasisPoints: 1900}, resolver)
	if _, err := s.ScanQty("CAPP", 1); err != nil {
		t.Fatalf("ScanQty error: %v", err)
	}
	s.SetTaxRateAsker(fakeTaxAsker{takeawayRateByTaxCode: map[string]int{"tax-drink": 700}})

	dineIn := s.Basket()
	if dineIn.Total != money.FromMinor(480) {
		t.Fatalf("dine-in Total = %v, want 4.80 unchanged", dineIn.Total)
	}

	takeaway := *s.SetOrderType(OrderTypeTakeaway)
	if takeaway.Total != dineIn.Total {
		t.Fatalf("takeaway Total = %v, want unchanged from dine-in %v (gross-inclusive invariant broken)", takeaway.Total, dineIn.Total)
	}
	if takeaway.Tax == dineIn.Tax {
		t.Fatalf("takeaway Tax = %v, same as dine-in %v — rate switch had no effect, test is not exercising the invariant", takeaway.Tax, dineIn.Tax)
	}
	if len(takeaway.Lines) != 1 || takeaway.Lines[0].LineTotal != money.FromMinor(480) {
		t.Fatalf("takeaway line gross = %+v, want unchanged 4.80", takeaway.Lines)
	}

	// Switch back: dine-in totals must be bit-identical to the original.
	backToDineIn := *s.SetOrderType("")
	if backToDineIn.Total != dineIn.Total || backToDineIn.Tax != dineIn.Tax {
		t.Fatalf("reverted dine-in = %+v, want Total=%v Tax=%v", backToDineIn, dineIn.Total, dineIn.Tax)
	}
}

// TestGrossInclusiveInvariant_MultiLineSumIdentity covers the rounding
// acceptance criterion directly: with several lines at odd prices and rates
// chosen to stress half-up rounding, the sum of each line's own displayed
// gross (LineTotal) must equal the basket total exactly — no per-line
// rounding of the derived net/tax may leak into (or accumulate against)
// the gross the customer sees summed at the bottom of the receipt.
func TestGrossInclusiveInvariant_MultiLineSumIdentity(t *testing.T) {
	lines := []struct {
		sku     string
		gross   money.Money
		rateBP  int
		newRate int
	}{
		{"A", money.FromMinor(479), 1900, 700}, // odd gross, DE pair
		{"B", money.FromMinor(333), 1000, 550}, // odd gross, odd rates
		{"C", money.FromMinor(1), 2000, 0},     // smallest unit, rate to zero
		{"D", money.FromMinor(10001), 750, 1925},
	}

	resolver := mapResolver{}
	takeawayByCode := map[string]int{}
	for _, l := range lines {
		resolver[l.sku] = BasketLine{SKU: l.sku, ItemID: "item-" + l.sku, TaxCodeID: "tax-" + l.sku, Name: l.sku, Qty: 1, PriceCents: l.gross, TaxRateBP: l.rateBP}
		takeawayByCode["tax-"+l.sku] = l.newRate
	}
	s := NewServiceWithResolver(Config{TaxInclusive: true}, resolver)
	for _, l := range lines {
		if _, err := s.ScanQty(l.sku, 1); err != nil {
			t.Fatalf("ScanQty(%s) error: %v", l.sku, err)
		}
	}
	s.SetTaxRateAsker(fakeTaxAsker{takeawayRateByTaxCode: takeawayByCode})

	var wantSum money.Money
	for _, l := range lines {
		wantSum = wantSum.Add(l.gross)
	}

	before := s.Basket()
	if before.Total != wantSum {
		t.Fatalf("before switch: Total = %v, want sum-of-displayed-gross %v", before.Total, wantSum)
	}

	after := *s.SetOrderType(OrderTypeTakeaway)
	if after.Total != wantSum {
		t.Fatalf("after switch: Total = %v, want sum-of-displayed-gross %v (rounding drift or gross moved)", after.Total, wantSum)
	}
	var lineSum money.Money
	for _, bl := range after.Lines {
		lineSum = lineSum.Add(bl.LineTotal)
	}
	if lineSum != after.Total {
		t.Fatalf("sum of line.LineTotal = %v, != Basket.Total %v", lineSum, after.Total)
	}
}

// TestVATBandsForSale_InclusiveGrossSumMatchesSaleTotal is the cross-surface
// check: VATBandsForSale is the single function shared by the invoice VAT
// table AND the day-close Z-report (see vat_breakdown.go's own doc
// comment), and the receipt is rendered from the same recorded
// total-after-tax figures. So proving its band Gross sums correctly for a
// mixed-rate, tax-inclusive sale — WITH a whole-sale discount and a service
// charge, so both proration branches (discount apportionment, ADR-0061
// service-charge apportionment) actually run, not just the pass-through sum
// — is proving receipt/day-close/invoice agree on the same gross; they have
// no separate code path to disagree through. (A zero-discount/zero-charge
// version of this test would be a no-op — see git history/review record for
// why that shape was rejected.)
func TestVATBandsForSale_InclusiveGrossSumMatchesSaleTotal(t *testing.T) {
	// Two lines recorded at different DE rates (dine-in 19%, takeaway 7%),
	// as CompleteSale would persist them: LineTotal is gross either way.
	line1Gross := money.FromMinor(480)
	line1Tax, _ := ComputeTaxBasisPoints(line1Gross, 1900, true)
	line2Gross := money.FromMinor(300)
	line2Tax, _ := ComputeTaxBasisPoints(line2Gross, 700, true)

	const discount int64 = 50         // whole-sale discount, minor units
	const serviceCharge int64 = 78    // 10% of (780-50), minor units
	const serviceChargeTaxBasisBP = 0 // no plugin override: fail-closed default (per-line rates)

	bands := VATBandsForSale([]VATLine{
		{RateBP: 1900, LineTotal: line1Gross.Minor(), TaxAmount: line1Tax.Minor()},
		{RateBP: 700, LineTotal: line2Gross.Minor(), TaxAmount: line2Tax.Minor()},
	}, discount, true, serviceCharge, serviceChargeTaxBasisBP)

	if len(bands) == 0 {
		t.Fatalf("no bands returned")
	}
	var grossSum int64
	for _, b := range bands {
		if b.Net+b.Tax != b.Gross {
			t.Fatalf("band %+v: Net+Tax != Gross", b)
		}
		grossSum += b.Gross
	}
	// What the customer actually paid: sum of line gross, minus the
	// whole-sale discount, plus the service charge — the same identity
	// recomputeTotals (service.go) and CompleteSale must both honour for
	// the on-screen total to match what gets recorded/receipted.
	want := line1Gross.Minor() + line2Gross.Minor() - discount + serviceCharge
	if grossSum != want {
		t.Fatalf("sum of band.Gross = %d, want %d (sale's actual recorded gross, discount+service-charge inclusive)", grossSum, want)
	}
}

// TestGrossInclusiveInvariant_WithDiscountAndServiceCharge closes the
// coverage gap an independent review flagged (ut-docs#1014): recomputeTotals
// adds the service charge's OWN tax (chargeTax) to Basket.Total only when
// the catalog is tax-EXCLUSIVE (service.go, around the `!s.cfg.TaxInclusive`
// branch) — for an inclusive catalog it's folded entirely into Basket.Tax
// instead. That's the single line most likely to silently break the
// gross-inclusive invariant under a future edit, and neither of this file's
// other Service-level tests exercises it: this one configures both a
// service charge and a discount and switches order type across them.
// TestBasketTax_InclusiveDiscountMatchesPersistedSaleTax closes the gap an
// independent review of ut-docs#1035 found: computeSaleTotals (sales.go)
// was fixed to derive taxTotal via VATBandsForSale, but recomputeTotals
// (service.go) -- the live basket panel a cashier sees BEFORE tender --
// still accumulated tax as a flat per-line sum. A cashier applying a
// whole-sale discount to an inclusive-priced sale would see a DIFFERENT
// tax figure on screen than what CompleteSale would persist and the
// printed receipt would show. This pins the basket panel to the same
// ticket reproduction sales_test.go uses: €11.90 inclusive @19% with a
// €1.90 whole-sale discount, tax must read 1.60, not 1.90.
func TestBasketTax_InclusiveDiscountMatchesPersistedSaleTax(t *testing.T) {
	resolver := mapResolver{
		"WIDGET": {SKU: "WIDGET", ItemID: "item-widget", TaxCodeID: "tax-std", Name: "Widget", Qty: 1, PriceCents: money.FromMinor(1190), TaxRateBP: 1900},
	}
	s := NewServiceWithResolver(Config{TaxInclusive: true}, resolver)
	if _, err := s.ScanQty("WIDGET", 1); err != nil {
		t.Fatalf("ScanQty: %v", err)
	}
	s.SetDiscount(money.FromMinor(190))

	b := s.Basket()
	if b.Total != money.FromMinor(1000) {
		t.Fatalf("basket Total = %v, want 10.00 (11.90 - 1.90 discount)", b.Total)
	}
	if b.Tax != money.FromMinor(160) {
		t.Fatalf("basket Tax = %v, want 1.60 (was 1.90 before ut-docs#1035's fix reached the live basket panel)", b.Tax)
	}
}

func TestGrossInclusiveInvariant_WithDiscountAndServiceCharge(t *testing.T) {
	resolver := mapResolver{
		"CAPP": {SKU: "CAPP", ItemID: "item-capp", TaxCodeID: "tax-drink", Name: "Cappuccino", Qty: 1, PriceCents: money.FromMinor(480), TaxRateBP: 1900},
		"CAKE": {SKU: "CAKE", ItemID: "item-cake", TaxCodeID: "tax-cake", Name: "Cake", Qty: 1, PriceCents: money.FromMinor(300), TaxRateBP: 700},
	}
	s := NewServiceWithResolver(Config{TaxInclusive: true, ServiceChargeRateBasisPoints: 1000}, resolver)
	for _, sku := range []string{"CAPP", "CAKE"} {
		if _, err := s.ScanQty(sku, 1); err != nil {
			t.Fatalf("ScanQty(%s) error: %v", sku, err)
		}
	}
	s.SetDiscount(money.FromMinor(50))
	s.SetTaxRateAsker(fakeTaxAsker{takeawayRateByTaxCode: map[string]int{"tax-drink": 700}})

	dineIn := s.Basket()
	if dineIn.ServiceCharge == 0 {
		t.Fatalf("service charge = 0, test setup didn't actually configure one")
	}
	if dineIn.Discount != money.FromMinor(50) {
		t.Fatalf("discount = %v, want 0.50", dineIn.Discount)
	}

	takeaway := *s.SetOrderType(OrderTypeTakeaway)
	if takeaway.Total != dineIn.Total {
		t.Fatalf("with discount+service-charge: takeaway Total = %v, want unchanged from dine-in %v (gross-inclusive invariant broken)", takeaway.Total, dineIn.Total)
	}
	if takeaway.Tax == dineIn.Tax {
		t.Fatalf("takeaway Tax = %v, same as dine-in %v — rate switch had no effect", takeaway.Tax, dineIn.Tax)
	}
	if takeaway.ServiceCharge != dineIn.ServiceCharge {
		t.Fatalf("ServiceCharge moved across the switch: %v -> %v, want unchanged (its rate isn't order-type dependent)", dineIn.ServiceCharge, takeaway.ServiceCharge)
	}
}
