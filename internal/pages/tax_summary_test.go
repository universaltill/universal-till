package pages

import (
	"context"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/pos"
)

// TestTaxSummary_AgreesWithEODTaxBands_WholeSaleDiscountInclusive is the
// ut-docs#1115 acceptance test: the Reports "Tax" tab and the day-close
// Z-report must show the SAME VAT bands for an inclusive-priced sale
// carrying a whole-sale discount — the exact shape that used to diverge
// (old TaxSummary's raw sale_lines sum never saw the discount at all,
// while the Z-report's bands, via pos.VATBandsForSale, correctly prorated
// it). Same fixture as
// TestEODTaxBands_WholeSaleDiscountInclusiveThroughCompleteSale: €11.90
// @19% inclusive with a €1.90 whole-sale discount, through the REAL
// pos.CompleteSale — not a hand-inserted row — so this exercises the
// production write path, not just the read side.
func TestTaxSummary_AgreesWithEODTaxBands_WholeSaleDiscountInclusive(t *testing.T) {
	d := etbOpenDB(t, "tax-summary-agrees.db")
	etbItem(t, d, "itm-disc-incl", 1190)
	repo := data.NewPOSRepo(d.DB)

	day := etbCompleteSale(t, d, pos.SaleInput{
		SaleType: "sale", Currency: "EUR", TaxInclusive: true,
		SaleDiscount:           money.FromMinor(190),
		AllowNegativeInventory: true,
		Lines: []pos.SaleLineInput{{
			ItemID: "itm-disc-incl", Name: "Widget", Qty: 1,
			UnitPrice: money.FromMinor(1190), TaxRateBasisPoints: 1900, LocationID: "loc_main",
		}},
		Payments: []pos.PaymentInput{{MethodID: "cash", Amount: money.FromMinor(1000)}},
	})

	// Z-report side: the existing production path.
	rep := etbEndOfDay(t, d, day)
	if len(rep.TaxBands) != 1 {
		t.Fatalf("Z-report: expected 1 band, got %+v", rep.TaxBands)
	}

	// Tax tab side: computeTaxSummary over a window that comfortably
	// contains "now" (etbCompleteSale stamps the real wall clock, not a
	// controllable fixture time).
	from := time.Now().Add(-time.Hour)
	to := time.Now().Add(time.Hour)
	taxRows, err := computeTaxSummary(context.Background(), repo, from, to)
	if err != nil {
		t.Fatalf("computeTaxSummary: %v", err)
	}
	if len(taxRows) != 1 {
		t.Fatalf("Tax tab: expected 1 band, got %+v", taxRows)
	}

	// The load-bearing assertion: same rate, same net, same tax, same
	// gross — the Tax tab must not be able to disagree with the Z-report
	// for the sales they both describe.
	if taxRows[0] != rep.TaxBands[0] {
		t.Fatalf("Tax tab band %+v != Z-report band %+v — ut-docs#1115 regression", taxRows[0], rep.TaxBands[0])
	}
	if want := (data.TaxBand{RateBP: 1900, Net: 840, Tax: 160, Gross: 1000}); taxRows[0] != want {
		t.Fatalf("band = %+v, want %+v (gross 1190-190=1000, tax re-derived at 19%%, matching ut-docs#1035)", taxRows[0], want)
	}
}

// TestTaxSummary_AgreesWithEODTaxBands_ServiceChargeAndReturn closes the
// review-flagged gap (ut-docs#1115 review, F5): the discount test above
// only exercises ONE of the two sale-level amounts the old TaxSummary
// couldn't see, and the composition of computeTaxSummary with a RETURN in
// the same window was, before this test, only asserted on the raw-fetch
// side (SalesForTaxWindow returns it unsigned) and the Z-report side
// (the sign-flip itself) separately — nothing proved the two compose
// correctly through computeTaxSummary end to end.
//
// One window, two real sales: a service-charge sale (the other sale-level
// amount with no sale_lines row) and a return of a plain taxed item.
// computeTaxSummary must merge and sign-flip exactly the way
// EndOfDayRange's Z-report bands do for the same range.
func TestTaxSummary_AgreesWithEODTaxBands_ServiceChargeAndReturn(t *testing.T) {
	d := etbOpenDB(t, "tax-summary-agrees-charge-return.db")
	etbItem(t, d, "itm-sc2", 1190)
	etbItem(t, d, "itm-ret", 1000)
	repo := data.NewPOSRepo(d.DB)

	// €11.90 @19% inclusive + €10.00 service charge (apportioned basis) —
	// same shape as TestEODTaxBands_ServiceChargeSaleThroughCompleteSale.
	day1 := etbCompleteSale(t, d, pos.SaleInput{
		SaleType: "sale", Currency: "EUR", TaxInclusive: true,
		ServiceCharge:          money.FromMinor(1000),
		AllowNegativeInventory: true,
		Lines: []pos.SaleLineInput{{
			ItemID: "itm-sc2", Name: "Dinner", Qty: 1,
			UnitPrice: money.FromMinor(1190), TaxRateBasisPoints: 1900, LocationID: "loc_main",
		}},
		Payments: []pos.PaymentInput{{MethodID: "cash", Amount: money.FromMinor(2190)}},
	})
	// A return of a plain 19% item: net 1000 tax 190.
	day2 := etbCompleteSale(t, d, pos.SaleInput{
		SaleType: "return", Currency: "EUR", TaxInclusive: false,
		AllowNegativeInventory: true,
		Lines: []pos.SaleLineInput{{
			ItemID: "itm-ret", Name: "Widget", Qty: 1,
			UnitPrice: money.FromMinor(1000), TaxRateBasisPoints: 1900, LocationID: "loc_main",
		}},
		// netPayments requires a positive Amount regardless of sale type
		// (it validates the payment ROW, not the sale's net effect) — a
		// return's Amount is the positive sum handed back, matching
		// refund_page.go's own refundTotal convention.
		Payments: []pos.PaymentInput{{MethodID: "cash", Amount: money.FromMinor(1190)}},
	})

	// Z-report side over the range both sales landed in.
	rep, err := repo.EndOfDayRange(context.Background(), day1, day2)
	if err != nil {
		t.Fatalf("EndOfDayRange: %v", err)
	}
	if err := attachEODTaxBands(context.Background(), repo, &rep); err != nil {
		t.Fatalf("attachEODTaxBands: %v", err)
	}

	// Tax tab side: same real-clock window as the test above.
	from := time.Now().Add(-time.Hour)
	to := time.Now().Add(time.Hour)
	taxRows, err := computeTaxSummary(context.Background(), repo, from, to)
	if err != nil {
		t.Fatalf("computeTaxSummary: %v", err)
	}

	if len(taxRows) != len(rep.TaxBands) {
		t.Fatalf("Tax tab bands %+v, Z-report bands %+v — different band counts", taxRows, rep.TaxBands)
	}
	for i := range rep.TaxBands {
		if taxRows[i] != rep.TaxBands[i] {
			t.Fatalf("Tax tab band[%d] = %+v != Z-report band[%d] = %+v — service-charge/return composition regression", i, taxRows[i], i, rep.TaxBands[i])
		}
	}
	// One band, net: sale's line 1000 + charge 840 - return's 1000 = 840;
	// tax: sale's line 190 + charge 160 - return's 190 = 160.
	if want := (data.TaxBand{RateBP: 1900, Net: 840, Tax: 160, Gross: 1000}); len(taxRows) != 1 || taxRows[0] != want {
		t.Fatalf("bands = %+v, want single band %+v", taxRows, want)
	}
}
