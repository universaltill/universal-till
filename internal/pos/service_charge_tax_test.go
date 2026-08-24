package pos

import (
	"context"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/money"
)

// ADR-0060 Decision 2: a service charge is taxed at the sale's own blended
// per-line rates, apportioned across rate bands BY NET LINE VALUE (not
// gross), each band's tax computed at that band's own rate, with the
// largest-remainder correction landing the pennies on the last (highest)
// band — the same rounding-fairness shape as invoice_page.go's vatBreakdown,
// adapted from "discount by gross share" to "charge by net share".

func TestApportionServiceChargeTax_ExclusiveMultiBandByNetShare(t *testing.T) {
	lines := []ChargeTaxLine{
		{RateBP: 1900, Net: 1000},
		{RateBP: 700, Net: 500},
	}
	bands := ApportionServiceChargeTax(300, lines, false, 0)
	if len(bands) != 2 {
		t.Fatalf("want 2 bands, got %+v", bands)
	}
	// Sorted ascending by rate; net shares 500:1000 → 100:200.
	if bands[0].RateBP != 700 || bands[0].Amount != 100 || bands[0].Tax != 7 {
		t.Fatalf("700bp band: got %+v, want amount 100 tax 7", bands[0])
	}
	if bands[1].RateBP != 1900 || bands[1].Amount != 200 || bands[1].Tax != 38 {
		t.Fatalf("1900bp band: got %+v, want amount 200 tax 38", bands[1])
	}
	if got := ServiceChargeTax(300, lines, false, 0); got != 45 {
		t.Fatalf("ServiceChargeTax = %d, want 45 (7+38)", got)
	}
}

func TestApportionServiceChargeTax_LargestRemainderLandsOnLastBand(t *testing.T) {
	lines := []ChargeTaxLine{
		{RateBP: 500, Net: 1000},
		{RateBP: 1000, Net: 1000},
		{RateBP: 2000, Net: 1000},
	}
	bands := ApportionServiceChargeTax(100, lines, false, 0)
	if len(bands) != 3 {
		t.Fatalf("want 3 bands, got %+v", bands)
	}
	// Equal thirds of 100 can't split evenly: floor shares 33/33, the last
	// (highest-rate) band absorbs the remainder — and the shares MUST sum
	// back to exactly the charge, no minor unit lost or invented.
	if bands[0].Amount != 33 || bands[1].Amount != 33 || bands[2].Amount != 34 {
		t.Fatalf("want shares 33/33/34, got %d/%d/%d", bands[0].Amount, bands[1].Amount, bands[2].Amount)
	}
	var sum money.Money
	for _, b := range bands {
		sum = sum.Add(b.Amount)
	}
	if sum != 100 {
		t.Fatalf("band amounts must sum to the charge exactly, got %d", sum)
	}
}

// Inclusive pricing: the weights are each band's TRUE (tax-exclusive) net,
// not the gross the line total holds. With lines 1190@19% (true net 1000)
// and 107@7% (true net 100), a 110 charge splits 100:10 by net — gross
// weighting would give the 7% band only 9. The allocated share stays in the
// sale's own pricing mode (tax embedded), so each band's tax is the amount's
// contained tax at that band's rate.
func TestApportionServiceChargeTax_InclusiveWeightsByTrueNet(t *testing.T) {
	lines := []ChargeTaxLine{
		{RateBP: 1900, Net: 1190},
		{RateBP: 700, Net: 107},
	}
	bands := ApportionServiceChargeTax(110, lines, true, 0)
	if len(bands) != 2 {
		t.Fatalf("want 2 bands, got %+v", bands)
	}
	if bands[0].RateBP != 700 || bands[0].Amount != 10 || bands[0].Tax != 1 {
		t.Fatalf("700bp band: got %+v, want amount 10 (net-weighted, not 9 gross-weighted) tax 1", bands[0])
	}
	if bands[1].RateBP != 1900 || bands[1].Amount != 100 || bands[1].Tax != 16 {
		t.Fatalf("1900bp band: got %+v, want amount 100 tax 16", bands[1])
	}
}

// A plugin's charge.policy.ask answer may fix a flat tax basis for the
// whole charge (service_charge_tax_basis_bp > 0) — one band at that rate,
// no apportionment.
func TestApportionServiceChargeTax_FlatBasisOverridesApportionment(t *testing.T) {
	lines := []ChargeTaxLine{
		{RateBP: 1900, Net: 1000},
		{RateBP: 700, Net: 500},
	}
	bands := ApportionServiceChargeTax(200, lines, false, 700)
	if len(bands) != 1 {
		t.Fatalf("want a single flat band, got %+v", bands)
	}
	if bands[0].RateBP != 700 || bands[0].Amount != 200 || bands[0].Tax != 14 {
		t.Fatalf("flat band: got %+v, want rate 700 amount 200 tax 14", bands[0])
	}
}

func TestApportionServiceChargeTax_NoChargeNoBands(t *testing.T) {
	lines := []ChargeTaxLine{{RateBP: 1900, Net: 1000}}
	if bands := ApportionServiceChargeTax(0, lines, false, 0); bands != nil {
		t.Fatalf("zero charge: want nil bands, got %+v", bands)
	}
	if bands := ApportionServiceChargeTax(-5, lines, false, 0); bands != nil {
		t.Fatalf("negative charge: want nil bands, got %+v", bands)
	}
}

// All-zero-value lines leave nothing to weight by; the whole charge lands on
// the highest rate present — the conservative direction that can never
// under-declare (ADR-0060's fail-closed reasoning).
func TestApportionServiceChargeTax_ZeroWeightsFallToHighestBand(t *testing.T) {
	lines := []ChargeTaxLine{
		{RateBP: 700, Net: 0},
		{RateBP: 1900, Net: 0},
	}
	bands := ApportionServiceChargeTax(100, lines, false, 0)
	if len(bands) != 2 {
		t.Fatalf("want 2 bands, got %+v", bands)
	}
	if bands[0].Amount != 0 || bands[1].RateBP != 1900 || bands[1].Amount != 100 || bands[1].Tax != 19 {
		t.Fatalf("want the whole charge on the 1900bp band, got %+v", bands)
	}
}

// --- CompleteSale: the untaxed service charge path is UNREACHABLE ----------

// ADR-0060 Decision 2's required proof: with NO plugin installed (internal/pos
// has no plugin subsystem at all — nothing here can answer charge.policy.ask),
// the service charge is STILL taxed at the sale's blended per-line rates. The
// old untaxed total (1300) must no longer even satisfy payment coverage, and
// the persisted totals must actually carry the charge's tax — not merely
// populate some side field.
func TestCompleteSale_ServiceChargeTaxedByDefault_UntaxedPathUnreachable(t *testing.T) {
	ctx := context.Background()
	db := setupSaleDB(t)
	defer db.Close()

	_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','SKU1','Steak', 1000, 1)`)
	_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv1','itm1',NULL,'loc1',5,datetime('now'))`)
	_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('cash','Cash','cash',1)`)

	baseIn := SaleInput{
		SaleType:      "sale",
		Currency:      "EUR",
		TaxInclusive:  false,
		ServiceCharge: 100,
		Lines: []SaleLineInput{
			{ItemID: "itm1", SKU: "SKU1", Name: "Steak", Qty: 1, UnitPrice: 1000, TaxRateBasisPoints: 2000, LocationID: "loc1"},
		},
	}

	// Pre-ADR-0060 total was 1000 + 100 + 200 = 1300 (charge untaxed). That
	// amount must now be an underpayment: the charge's 20% tax (20) is due.
	underpaid := baseIn
	underpaid.Payments = []PaymentInput{{MethodID: "cash", Amount: 1300}}
	if _, err := CompleteSale(ctx, db, underpaid); err == nil || !strings.Contains(err.Error(), "do not cover total") {
		t.Fatalf("payment at the old untaxed total must be rejected as underpayment, got err=%v", err)
	}

	paid := baseIn
	paid.Payments = []PaymentInput{{MethodID: "cash", Amount: 1320}}
	saleID, err := CompleteSale(ctx, db, paid)
	if err != nil {
		t.Fatalf("CompleteSale: %v", err)
	}
	var total, taxTotal, serviceCharge int64
	if err := db.QueryRow(`SELECT total, tax_total, service_charge_amount FROM sales WHERE id=?`, saleID).Scan(&total, &taxTotal, &serviceCharge); err != nil {
		t.Fatalf("read sale: %v", err)
	}
	if serviceCharge != 100 {
		t.Fatalf("service_charge_amount must stay the original charge amount (100), got %d", serviceCharge)
	}
	if taxTotal != 220 {
		t.Fatalf("tax_total must include the charge's apportioned tax (200 line + 20 charge), got %d", taxTotal)
	}
	if total != 1320 {
		t.Fatalf("total must include the charge's tax (1000+100+200+20), got %d", total)
	}
}

// Inclusive pricing: the charge amount already contains its tax, so the
// total is unchanged (1190 + 119) but tax_total must declare the tax
// embedded in the charge alongside the lines' own.
func TestCompleteSale_ServiceChargeTaxedInclusiveMode(t *testing.T) {
	ctx := context.Background()
	db := setupSaleDB(t)
	defer db.Close()

	_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','SKU1','Schnitzel', 1190, 1)`)
	_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv1','itm1',NULL,'loc1',5,datetime('now'))`)
	_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('cash','Cash','cash',1)`)

	in := SaleInput{
		SaleType:      "sale",
		Currency:      "EUR",
		TaxInclusive:  true,
		ServiceCharge: 119,
		Lines: []SaleLineInput{
			{ItemID: "itm1", SKU: "SKU1", Name: "Schnitzel", Qty: 1, UnitPrice: 1190, TaxRateBasisPoints: 1900, LocationID: "loc1"},
		},
		Payments: []PaymentInput{{MethodID: "cash", Amount: 1309}},
	}
	saleID, err := CompleteSale(ctx, db, in)
	if err != nil {
		t.Fatalf("CompleteSale: %v", err)
	}
	var total, taxTotal int64
	if err := db.QueryRow(`SELECT total, tax_total FROM sales WHERE id=?`, saleID).Scan(&total, &taxTotal); err != nil {
		t.Fatalf("read sale: %v", err)
	}
	if total != 1309 {
		t.Fatalf("inclusive total is gross-in gross-out (1190+119), got %d", total)
	}
	// Line's embedded tax 190 + the charge's embedded tax 19.
	if taxTotal != 209 {
		t.Fatalf("tax_total must declare the charge's embedded tax (190+19), got %d", taxTotal)
	}
}

// A charge.policy.ask answer's service_charge_tax_basis_bp (threaded into
// SaleInput by the tender handler) fixes a flat rate for the whole charge.
func TestCompleteSale_ServiceChargeFlatTaxBasisFromPolicy(t *testing.T) {
	ctx := context.Background()
	db := setupSaleDB(t)
	defer db.Close()

	_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','SKU1','Steak', 1000, 1)`)
	_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv1','itm1',NULL,'loc1',5,datetime('now'))`)
	_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('cash','Cash','cash',1)`)

	in := SaleInput{
		SaleType:                "sale",
		Currency:                "EUR",
		TaxInclusive:            false,
		ServiceCharge:           100,
		ServiceChargeTaxBasisBP: 700,
		Lines: []SaleLineInput{
			{ItemID: "itm1", SKU: "SKU1", Name: "Steak", Qty: 1, UnitPrice: 1000, TaxRateBasisPoints: 2000, LocationID: "loc1"},
		},
		Payments: []PaymentInput{{MethodID: "cash", Amount: 1307}},
	}
	saleID, err := CompleteSale(ctx, db, in)
	if err != nil {
		t.Fatalf("CompleteSale: %v", err)
	}
	var total, taxTotal int64
	if err := db.QueryRow(`SELECT total, tax_total FROM sales WHERE id=?`, saleID).Scan(&total, &taxTotal); err != nil {
		t.Fatalf("read sale: %v", err)
	}
	if taxTotal != 207 {
		t.Fatalf("tax_total: want 207 (200 line + 7 flat-basis charge tax), got %d", taxTotal)
	}
	if total != 1307 {
		t.Fatalf("total: want 1307, got %d", total)
	}
}

// --- Basket preview stays in step with CompleteSale ------------------------

// The live basket (Service.recomputeTotals) must quote the same
// charge-taxed total the tender path will demand, or an exact-amount cash
// tender read off the screen gets rejected as underpayment.
func TestService_BasketPreviewIncludesServiceChargeTax(t *testing.T) {
	s := NewServiceWithResolver(Config{
		TaxInclusive:                 false,
		TaxRateBasisPoints:           2000,
		ServiceChargeRateBasisPoints: 1000,
	}, mapResolver{
		"ABC": {SKU: "ABC", Name: "Steak", Qty: 1, PriceCents: 1000, TaxRateBP: 2000},
	})
	if _, err := s.Scan("ABC"); err != nil {
		t.Fatalf("scan: %v", err)
	}
	b := s.Basket()
	if b.ServiceCharge != 100 {
		t.Fatalf("service charge: want 100 (10%% of 1000), got %d", b.ServiceCharge)
	}
	if b.Tax != 220 {
		t.Fatalf("basket tax must include the charge's tax (200+20), got %d", b.Tax)
	}
	if b.Total != 1320 {
		t.Fatalf("basket total must match CompleteSale's charge-taxed total (1320), got %d", b.Total)
	}
}

// fakeChargePolicyAsker is an in-package ChargePolicyAsker stub.
type fakeChargePolicyAsker struct {
	policy   ChargePolicy
	answered bool
}

func (f *fakeChargePolicyAsker) AskChargePolicy() (ChargePolicy, bool) {
	return f.policy, f.answered
}

// A charge.policy.ask answer drives the preview: permitted=false suppresses
// the charge entirely; a flat tax basis replaces the per-line apportionment.
func TestService_ChargePolicyAnswerDrivesPreview(t *testing.T) {
	newSvc := func() *Service {
		s := NewServiceWithResolver(Config{
			TaxInclusive:                 false,
			TaxRateBasisPoints:           2000,
			ServiceChargeRateBasisPoints: 1000,
		}, mapResolver{
			"ABC": {SKU: "ABC", Name: "Steak", Qty: 1, PriceCents: 1000, TaxRateBP: 2000},
		})
		if _, err := s.Scan("ABC"); err != nil {
			t.Fatalf("scan: %v", err)
		}
		return s
	}

	// Not permitted: no charge, no charge tax.
	s := newSvc()
	s.SetChargePolicyAsker(&fakeChargePolicyAsker{
		policy:   ChargePolicy{ServiceChargePermitted: false},
		answered: true,
	})
	if b := s.Basket(); b.ServiceCharge != 0 || b.Total != 1200 || b.Tax != 200 {
		t.Fatalf("not-permitted answer must suppress the charge: got charge %d tax %d total %d", b.ServiceCharge, b.Tax, b.Total)
	}

	// Permitted with a flat 7% tax basis for the charge.
	s = newSvc()
	s.SetChargePolicyAsker(&fakeChargePolicyAsker{
		policy: ChargePolicy{
			ServiceChargePermitted:  true,
			ServiceChargeTaxBasisBP: 700,
		},
		answered: true,
	})
	if b := s.Basket(); b.ServiceCharge != 100 || b.Tax != 207 || b.Total != 1307 {
		t.Fatalf("flat-basis answer: got charge %d tax %d total %d, want 100/207/1307", b.ServiceCharge, b.Tax, b.Total)
	}

	// No answer (no plugin): the fail-closed per-line default applies.
	s = newSvc()
	s.SetChargePolicyAsker(&fakeChargePolicyAsker{answered: false})
	if b := s.Basket(); b.ServiceCharge != 100 || b.Tax != 220 || b.Total != 1320 {
		t.Fatalf("no answer: got charge %d tax %d total %d, want the taxed default 100/220/1320", b.ServiceCharge, b.Tax, b.Total)
	}
}

// --- Tip recipient (ADR-0060 Decision 3) -----------------------------------

func TestCompleteSale_TipRecipientDefaultsToEmployee(t *testing.T) {
	ctx := context.Background()
	db := setupSaleDB(t)
	defer db.Close()
	_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','SKU1','Coffee', 370, 1)`)
	_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv1','itm1',NULL,'loc1',5,datetime('now'))`)
	_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('card','Card','card',1)`)

	in := SaleInput{
		SaleType: "sale",
		Currency: "GBP",
		Lines: []SaleLineInput{
			{ItemID: "itm1", SKU: "SKU1", Name: "Coffee", Qty: 1, UnitPrice: 370, TaxRateBasisPoints: 0, LocationID: "loc1"},
		},
		// No TipRecipient set anywhere — the one default every researched
		// market agrees on is "employee".
		Payments: []PaymentInput{{MethodID: "card", Amount: 420, TipAmount: 50}},
	}
	saleID, err := CompleteSale(ctx, db, in)
	if err != nil {
		t.Fatalf("CompleteSale: %v", err)
	}
	var recipient string
	if err := db.QueryRow(`SELECT tip_recipient FROM payments WHERE sale_id=?`, saleID).Scan(&recipient); err != nil {
		t.Fatalf("read payment: %v", err)
	}
	if recipient != TipRecipientEmployee {
		t.Fatalf("want tip_recipient %q by default, got %q", TipRecipientEmployee, recipient)
	}
}

func TestCompleteSale_TipRecipientPersistsBusiness(t *testing.T) {
	ctx := context.Background()
	db := setupSaleDB(t)
	defer db.Close()
	_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','SKU1','Coffee', 370, 1)`)
	_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv1','itm1',NULL,'loc1',5,datetime('now'))`)
	_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('card','Card','card',1)`)

	in := SaleInput{
		SaleType: "sale",
		Currency: "GBP",
		Lines: []SaleLineInput{
			{ItemID: "itm1", SKU: "SKU1", Name: "Coffee", Qty: 1, UnitPrice: 370, TaxRateBasisPoints: 0, LocationID: "loc1"},
		},
		Payments: []PaymentInput{{MethodID: "card", Amount: 420, TipAmount: 50, TipRecipient: TipRecipientBusiness}},
	}
	saleID, err := CompleteSale(ctx, db, in)
	if err != nil {
		t.Fatalf("CompleteSale: %v", err)
	}
	var recipient string
	if err := db.QueryRow(`SELECT tip_recipient FROM payments WHERE sale_id=?`, saleID).Scan(&recipient); err != nil {
		t.Fatalf("read payment: %v", err)
	}
	if recipient != TipRecipientBusiness {
		t.Fatalf("want tip_recipient %q, got %q", TipRecipientBusiness, recipient)
	}
}

func TestCompleteSale_RejectsInvalidTipRecipient(t *testing.T) {
	ctx := context.Background()
	db := setupSaleDB(t)
	defer db.Close()
	_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','SKU1','Coffee', 370, 1)`)
	_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv1','itm1',NULL,'loc1',5,datetime('now'))`)
	_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('card','Card','card',1)`)

	in := SaleInput{
		SaleType: "sale",
		Currency: "GBP",
		Lines: []SaleLineInput{
			{ItemID: "itm1", SKU: "SKU1", Name: "Coffee", Qty: 1, UnitPrice: 370, TaxRateBasisPoints: 0, LocationID: "loc1"},
		},
		Payments: []PaymentInput{{MethodID: "card", Amount: 420, TipAmount: 50, TipRecipient: "the-till-itself"}},
	}
	if _, err := CompleteSale(ctx, db, in); err == nil {
		t.Fatal("expected an invalid tip_recipient to be rejected (validate all external input)")
	}
}
