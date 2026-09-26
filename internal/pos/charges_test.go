package pos

import (
	"context"
	"reflect"
	"testing"

	"github.com/universaltill/universal-till/internal/money"
)

// omanPolicy is a test-injected Omani-shaped charge.policy.ask answer
// (ADR-0062 Non-goals: no real GCC plugin exists) — municipality 5% and
// tourism 4%, each applied verbatim on the net lines.
func omanPolicy() ChargePolicy {
	return ChargePolicy{
		ServiceChargePermitted: true,
		Charges: []ChargeItem{
			{Key: "municipality_tax", Label: "Municipality tax", DefaultRateBP: 500, Base: ChargeBaseNetLines},
			{Key: "tourism_tax", Label: "Tourism tax", DefaultRateBP: 400, TaxBasisBP: 500, Base: ChargeBaseNetLines},
		},
	}
}

func TestBuildCharges_NoAnswerIsMerchantItemOnly(t *testing.T) {
	got := BuildCharges(1000, 1000, false, ChargePolicy{}, false)
	want := []ChargeInput{{Key: ServiceChargeKey, Amount: 100, Base: ChargeBaseNetLines}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestBuildCharges_MerchantItemPlusPluginItemsInOrder(t *testing.T) {
	p := omanPolicy()
	p.ServiceChargeTaxBasisBP = 700
	got := BuildCharges(10000, 1000, false, p, true)
	want := []ChargeInput{
		{Key: ServiceChargeKey, Amount: 1000, TaxBasisBP: 700, Base: ChargeBaseNetLines},
		{Key: "municipality_tax", Label: "Municipality tax", Amount: 500, Base: ChargeBaseNetLines},
		{Key: "tourism_tax", Label: "Tourism tax", Amount: 400, TaxBasisBP: 500, Base: ChargeBaseNetLines},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// ADR-0062 Decision 3: the Turkey ban (ut-docs#962) covers the WHOLE list —
// a plugin must not be a side door for a banned charge line.
func TestBuildCharges_ForbiddenSuppressesWholeListIncludingPluginItems(t *testing.T) {
	if got := BuildCharges(10000, 1000, true, omanPolicy(), true); got != nil {
		t.Fatalf("forbidden must suppress every charge, got %+v", got)
	}
	if got := BuildCharges(10000, 1000, true, ChargePolicy{}, false); got != nil {
		t.Fatalf("forbidden with no answer must still be empty, got %+v", got)
	}
}

// service_charge_permitted=false drops ONLY the merchant-rate item — a
// statutory plugin levy is not the merchant's optional service charge.
func TestBuildCharges_NotPermittedDropsOnlyMerchantItem(t *testing.T) {
	p := omanPolicy()
	p.ServiceChargePermitted = false
	got := BuildCharges(10000, 1000, false, p, true)
	if len(got) != 2 || got[0].Key != "municipality_tax" || got[1].Key != "tourism_tax" {
		t.Fatalf("want only the two plugin levies, got %+v", got)
	}
}

// A zero charge is not a charge (no sale_charges row): a 0% merchant rate
// or a 0 bp plugin item is dropped, and so is a non-positive base
// (an over-discounted sale) — a negative "charge" would be a discount.
func TestBuildCharges_ZeroAndNonPositiveAmountsDropped(t *testing.T) {
	p := omanPolicy()
	p.Charges[0].DefaultRateBP = 0
	got := BuildCharges(10000, 0, false, p, true)
	if len(got) != 1 || got[0].Key != "tourism_tax" || got[0].Amount != 400 {
		t.Fatalf("want only tourism_tax 400, got %+v", got)
	}
	if got := BuildCharges(0, 1000, false, omanPolicy(), true); got != nil {
		t.Fatalf("zero base must build no charges, got %+v", got)
	}
	if got := BuildCharges(-500, 1000, false, omanPolicy(), true); got != nil {
		t.Fatalf("negative base must build no charges, got %+v", got)
	}
}

// Base other than net_lines is reserved (ADR-0062 Decision 1): core
// computes on net_lines and records the base it actually applied.
func TestBuildCharges_ReservedBaseComputedAsNetLines(t *testing.T) {
	p := ChargePolicy{ServiceChargePermitted: true, Charges: []ChargeItem{
		{Key: "levy", DefaultRateBP: 1000, Base: "net_lines_plus_prior_charges"},
	}}
	got := BuildCharges(1000, 500, false, p, true)
	if len(got) != 2 || got[1].Amount != 100 || got[1].Base != ChargeBaseNetLines {
		t.Fatalf("want levy 100 on net_lines, got %+v", got)
	}
}

func TestSaleInput_ChargesTotal(t *testing.T) {
	in := SaleInput{Charges: []ChargeInput{{Amount: 500}, {Amount: 400}}}
	if got := in.ChargesTotal(); got != money.Money(900) {
		t.Fatalf("ChargesTotal = %d, want 900", got)
	}
	if got := (&SaleInput{}).ChargesTotal(); got != 0 {
		t.Fatalf("empty ChargesTotal = %d, want 0", got)
	}
}

// ADR-0062 worked example (ut-docs#963 AC): Oman — 5% municipality + 4%
// tourism on the net lines, VAT 5% on the lines; test-injected charges, no
// plugin. OMR has 3 decimals: 10.000 OMR net = 10000 minor units.
func TestComputeSaleTotals_OmaniWorkedExample(t *testing.T) {
	lines := []SaleLineInput{{ItemID: "itm1", LocationID: "loc1", Qty: 1, UnitPrice: 10000, TaxRateBasisPoints: 500}}
	charges := BuildCharges(10000, 0, false, ChargePolicy{
		ServiceChargePermitted: true,
		Charges: []ChargeItem{
			{Key: "municipality_tax", DefaultRateBP: 500},
			{Key: "tourism_tax", DefaultRateBP: 400},
		},
	}, true)

	t.Run("exclusive", func(t *testing.T) {
		subtotal, tax, charge, _, total, err := computeSaleTotals(SaleInput{Lines: lines, Charges: charges})
		if err != nil {
			t.Fatalf("computeSaleTotals: %v", err)
		}
		// lines 10000 @5% -> 500 VAT; municipality 500 @5% -> 25;
		// tourism 400 @5% -> 20. Total 10000 + 900 + 545.
		if subtotal != 10000 || charge != 900 || tax != 545 || total != 11445 {
			t.Fatalf("got subtotal %d charge %d tax %d total %d, want 10000/900/545/11445", subtotal, charge, tax, total)
		}
	})
	t.Run("inclusive", func(t *testing.T) {
		// Same amounts priced gross: 10000 incl. 5% -> 476 VAT; each charge
		// carries its own VAT inside it: 500 -> 24, 400 -> 19.
		subtotal, tax, charge, _, total, err := computeSaleTotals(SaleInput{Lines: lines, Charges: charges, TaxInclusive: true})
		if err != nil {
			t.Fatalf("computeSaleTotals: %v", err)
		}
		if subtotal != 10000 || charge != 900 || tax != 519 || total != 10900 {
			t.Fatalf("got subtotal %d charge %d tax %d total %d, want 10000/900/519/10900", subtotal, charge, tax, total)
		}
	})
}

// ADR-0062 Decision 1: validation is per item — any negative charge is
// rejected (a discount wearing a charge's clothes would bypass the
// sale_discounts audit trail), even when the list's sum is positive.
func TestComputeSaleTotals_RejectsAnyNegativeCharge(t *testing.T) {
	in := SaleInput{
		Lines:   []SaleLineInput{{ItemID: "itm1", LocationID: "loc1", Qty: 1, UnitPrice: 1000}},
		Charges: []ChargeInput{{Key: ServiceChargeKey, Amount: 100}, {Key: "levy", Amount: -1}},
	}
	if _, _, _, _, _, err := computeSaleTotals(in); err == nil {
		t.Fatal("want an error for a negative charge item, got nil")
	}
}

// ADR-0062 Decision 2: CompleteSale writes the itemized sale_charges rows
// in the sale's transaction, and derives the sales pair from the SAME
// list: service_charge_amount = sum; service_charge_tax_basis_bp = the one
// charge's basis, else 0. A single service_charge sale keeps exactly the
// pre-ADR-0062 sales row values (the 1320 / 220 / 100 / 700-basis figures
// TestCompleteSale_ServiceChargeFlatTaxBasisFromPolicy pinned before this
// card, recomputed here: 1000 @20% + 100 charge @ flat 7%).
func TestCompleteSale_PersistsSaleChargesAndDerivedColumns(t *testing.T) {
	ctx := context.Background()
	db := setupSaleDB(t)
	defer db.Close()
	_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','SKU1','Steak', 1000, 1)`)
	_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv1','itm1',NULL,'loc1',50,datetime('now'))`)
	_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('cash','Cash','cash',1)`)

	type saleRow struct {
		total, tax, charge int64
		basis              int
	}
	type chargeRow struct {
		key, label, base string
		amount           int64
		basis            int
	}
	complete := func(t *testing.T, charges []ChargeInput, pay int64) (saleRow, []chargeRow) {
		t.Helper()
		id, err := CompleteSale(ctx, db, SaleInput{
			SaleType: "sale",
			Currency: "EUR",
			Charges:  charges,
			Lines:    []SaleLineInput{{ItemID: "itm1", SKU: "SKU1", Name: "Steak", Qty: 1, UnitPrice: 1000, TaxRateBasisPoints: 2000, LocationID: "loc1"}},
			Payments: []PaymentInput{{MethodID: "cash", Amount: money.FromMinor(pay)}},
		})
		if err != nil {
			t.Fatalf("CompleteSale: %v", err)
		}
		var s saleRow
		if err := db.QueryRow(`SELECT total, tax_total, service_charge_amount, service_charge_tax_basis_bp FROM sales WHERE id=?`, id).
			Scan(&s.total, &s.tax, &s.charge, &s.basis); err != nil {
			t.Fatalf("read sale: %v", err)
		}
		rows, err := db.Query(`SELECT key, label, base, amount_minor, tax_basis_bp FROM sale_charges WHERE sale_id=? ORDER BY seq`, id)
		if err != nil {
			t.Fatalf("read sale_charges: %v", err)
		}
		defer rows.Close()
		var cs []chargeRow
		for rows.Next() {
			var c chargeRow
			if err := rows.Scan(&c.key, &c.label, &c.base, &c.amount, &c.basis); err != nil {
				t.Fatalf("scan: %v", err)
			}
			cs = append(cs, c)
		}
		return s, cs
	}

	t.Run("no charges", func(t *testing.T) {
		s, cs := complete(t, nil, 1200)
		if s != (saleRow{1200, 200, 0, 0}) || len(cs) != 0 {
			t.Fatalf("got %+v rows %+v, want {1200 200 0 0} and no rows", s, cs)
		}
	})
	t.Run("one service charge unchanged", func(t *testing.T) {
		s, cs := complete(t, []ChargeInput{{Key: ServiceChargeKey, Amount: 100, TaxBasisBP: 700, Base: ChargeBaseNetLines}}, 1307)
		if s != (saleRow{1307, 207, 100, 700}) {
			t.Fatalf("sales row = %+v, want {1307 207 100 700} (pre-ADR-0062 values)", s)
		}
		if len(cs) != 1 || cs[0] != (chargeRow{ServiceChargeKey, "", ChargeBaseNetLines, 100, 700}) {
			t.Fatalf("sale_charges = %+v, want the one service_charge row", cs)
		}
	})
	t.Run("two charges", func(t *testing.T) {
		// 100 @ flat 7% (7) + 50 per-line @20% (10): tax 200+17, total 1367.
		s, cs := complete(t, []ChargeInput{
			{Key: ServiceChargeKey, Amount: 100, TaxBasisBP: 700},
			{Key: "levy", Label: "Levy", Amount: 50},
		}, 1367)
		if s != (saleRow{1367, 217, 150, 0}) {
			t.Fatalf("sales row = %+v, want {1367 217 150 0} (sum; basis 0 for 2+ charges)", s)
		}
		want := []chargeRow{
			{ServiceChargeKey, "", ChargeBaseNetLines, 100, 700},
			{"levy", "Levy", ChargeBaseNetLines, 50, 0},
		}
		if len(cs) != 2 || cs[0] != want[0] || cs[1] != want[1] {
			t.Fatalf("sale_charges = %+v, want %+v", cs, want)
		}
	})
	t.Run("negative charge rejected, nothing written", func(t *testing.T) {
		_, err := CompleteSale(ctx, db, SaleInput{
			SaleType: "sale", Currency: "EUR",
			Charges:  []ChargeInput{{Key: ServiceChargeKey, Amount: 100}, {Key: "levy", Amount: -10}},
			Lines:    []SaleLineInput{{ItemID: "itm1", SKU: "SKU1", Name: "Steak", Qty: 1, UnitPrice: 1000, LocationID: "loc1"}},
			Payments: []PaymentInput{{MethodID: "cash", Amount: 2000}},
		})
		if err == nil {
			t.Fatal("want a negative charge rejected")
		}
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sale_charges WHERE key='levy' AND amount_minor < 0`).Scan(&n); err != nil || n != 0 {
			t.Fatalf("negative charge row persisted: n=%d err=%v", n, err)
		}
	})
}
