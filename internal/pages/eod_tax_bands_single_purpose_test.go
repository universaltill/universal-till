package pages

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/print"
)

// Day-close VAT bands on single-purpose voucher days (ADR-0105, ut-docs#1037).
// These are the money-correctness tests the ADR calls out explicitly: the
// identities documented at the top of eod_tax_bands.go must hold on a day
// with a single-purpose ISSUE (Decision 3 — the issue is a taxable supply
// that is never a sale_lines row, so the re-derived bands must inject it)
// and on a day with a single-purpose REDEMPTION (Decision 4 — the
// redeeming sale's lines must be left OUT of the bands, with the
// GUTSCHEINE bucket supplying the reconciling delta). Every sale goes
// through the real pos.CompleteSale, with the voucher type stamped
// explicitly so no settings row is needed.

// etbWindowAround is the local-calendar-day `day` as a [from, to) instant
// window — what the Tax tab's rolling-window report takes.
func etbWindowAround(t *testing.T, day string) (time.Time, time.Time) {
	t.Helper()
	from, err := time.ParseInLocation("2006-01-02", day, time.Local)
	if err != nil {
		t.Fatalf("parse day %q: %v", day, err)
	}
	return from, from.AddDate(0, 0, 1)
}

// etbEndOfDayWithMethods runs EndOfDay + attachEODBands (TaxBands AND the
// method cross-tab from ONE read, the production shape).
func etbEndOfDayWithMethods(t *testing.T, repo *data.POSRepo, day string) data.EODReport {
	t.Helper()
	rep, err := repo.EndOfDay(context.Background(), day)
	if err != nil {
		t.Fatal(err)
	}
	if err := attachEODBands(context.Background(), repo, &rep); err != nil {
		t.Fatalf("attachEODBands: %v", err)
	}
	return rep
}

// assertMethodBandsReconcile pins the ut-docs#1004 row-sum identity: the
// method x rate cross-tab summed per rate reproduces TaxBands exactly —
// so a single-purpose voucher must be injected (or excluded) identically
// in both aggregations.
func assertMethodBandsReconcile(t *testing.T, rep data.EODReport) {
	t.Helper()
	perRate := map[int]data.TaxBand{}
	for _, c := range rep.MethodTaxBands {
		b := perRate[c.RateBP]
		b.RateBP = c.RateBP
		b.Net += c.Net
		b.Tax += c.Tax
		b.Gross += c.Gross
		perRate[c.RateBP] = b
	}
	if len(perRate) != len(rep.TaxBands) {
		t.Fatalf("method cross-tab covers %d rate(s), TaxBands %d: %+v vs %+v", len(perRate), len(rep.TaxBands), rep.MethodTaxBands, rep.TaxBands)
	}
	for _, b := range rep.TaxBands {
		if got := perRate[b.RateBP]; got != b {
			t.Fatalf("method cross-tab at %d bp sums to %+v, TaxBands has %+v", b.RateBP, got, b)
		}
	}
}

// A single-purpose ISSUE day. Sale 1: one 19% article (10.00 incl, tax
// 1.60) plus a single-purpose voucher of 11.90 taxed at issue at 19% (tax
// 1.90) — the voucher is NOT a sale_lines row. Sale 2: a multi-purpose
// voucher of 5.00 (the ut-docs#1008 liability, inside Net, in no band).
//
// Before this card the day-close re-derived bands from sale_lines alone and
// reported {1900: 840/160/1000} against a TaxNet of 350 — under-declaring
// the 1.90 collected on the voucher and breaking sum(band.Tax) == TaxNet.
func TestEODTaxBands_SinglePurposeIssueDay(t *testing.T) {
	d := etbOpenDB(t, "eod-bands-sp-issue.db")
	etbItem(t, d, "itm-sp", 1000)

	day := etbCompleteSale(t, d, pos.SaleInput{
		SaleType: "sale", Currency: "EUR", TaxInclusive: true, AllowNegativeInventory: true,
		Lines: []pos.SaleLineInput{{
			ItemID: "itm-sp", Name: "Widget", Qty: 1,
			UnitPrice: money.FromMinor(1000), TaxRateBasisPoints: 1900, LocationID: "loc_main",
		}},
		VoucherIssues: []pos.VoucherIssueInput{{
			VoucherID: "SP-ISS", Amount: money.FromMinor(1190),
			VoucherType: data.VoucherTypeSinglePurpose, TaxRateBP: 1900,
		}},
		Payments: []pos.PaymentInput{{MethodID: "cash", Amount: money.FromMinor(2190)}},
	})
	etbCompleteSale(t, d, pos.SaleInput{
		SaleType: "sale", Currency: "EUR", TaxInclusive: true,
		VoucherIssues: []pos.VoucherIssueInput{{VoucherID: "MP-ISS", Amount: money.FromMinor(500), VoucherType: data.VoucherTypeMultiPurpose}},
		Payments:      []pos.PaymentInput{{MethodID: "cash", Amount: money.FromMinor(500)}},
	})

	rep := etbEndOfDayWithMethods(t, data.NewPOSRepo(d.DB), day)
	if rep.TaxNet != 350 || rep.Net != 2690 {
		t.Fatalf("engine totals moved: TaxNet=%d Net=%d, want 350/2690", rep.TaxNet, rep.Net)
	}
	if rep.VouchersIssued != 500 || rep.VouchersIssuedCount != 1 {
		t.Fatalf("GUTSCHEINE issued = %d/%d, want 1/500 — only the multi-purpose voucher is a liability", rep.VouchersIssuedCount, rep.VouchersIssued)
	}
	if rep.VouchersSinglePurposeIssued != 1190 || rep.VouchersSinglePurposeIssuedCount != 1 {
		t.Fatalf("single-purpose issued = %d/%d, want 1/1190", rep.VouchersSinglePurposeIssuedCount, rep.VouchersSinglePurposeIssued)
	}
	if len(rep.TaxBands) != 1 {
		t.Fatalf("expected 1 band, got %+v", rep.TaxBands)
	}
	if want := (data.TaxBand{RateBP: 1900, Net: 1840, Tax: 350, Gross: 2190}); rep.TaxBands[0] != want {
		t.Fatalf("band = %+v, want %+v (article 840/160/1000 + single-purpose voucher 1000/190/1190)", rep.TaxBands[0], want)
	}
	assertEODTaxBandIdentities(t, rep)
	assertMethodBandsReconcile(t, rep)
}

// The same day under EXCLUSIVE pricing: the face value is still what the
// customer pays (gross), so the band is identical — only the header's
// subtotal (net basis) differs, and InferTaxInclusive must still read the
// sale as exclusive.
func TestEODTaxBands_SinglePurposeIssueDay_Exclusive(t *testing.T) {
	d := etbOpenDB(t, "eod-bands-sp-issue-excl.db")
	etbItem(t, d, "itm-sp", 1000)

	day := etbCompleteSale(t, d, pos.SaleInput{
		SaleType: "sale", Currency: "EUR", TaxInclusive: false, AllowNegativeInventory: true,
		Lines: []pos.SaleLineInput{{
			ItemID: "itm-sp", Name: "Widget", Qty: 1,
			UnitPrice: money.FromMinor(1000), TaxRateBasisPoints: 1900, LocationID: "loc_main",
		}},
		VoucherIssues: []pos.VoucherIssueInput{{
			VoucherID: "SP-ISS-X", Amount: money.FromMinor(1190),
			VoucherType: data.VoucherTypeSinglePurpose, TaxRateBP: 1900,
		}},
		Payments: []pos.PaymentInput{{MethodID: "cash", Amount: money.FromMinor(2380)}},
	})
	rep := etbEndOfDayWithMethods(t, data.NewPOSRepo(d.DB), day)
	if rep.TaxNet != 380 || rep.Net != 2380 {
		t.Fatalf("engine totals moved: TaxNet=%d Net=%d, want 380/2380", rep.TaxNet, rep.Net)
	}
	if want := (data.TaxBand{RateBP: 1900, Net: 2000, Tax: 380, Gross: 2380}); len(rep.TaxBands) != 1 || rep.TaxBands[0] != want {
		t.Fatalf("bands = %+v, want [%+v]", rep.TaxBands, want)
	}
	assertEODTaxBandIdentities(t, rep)
	assertMethodBandsReconcile(t, rep)
}

// A single-purpose REDEMPTION day. The voucher (10.00 @19%) was sold the
// same day (its issue IS taxed: 840/160/1000), then redeemed as the sole
// exact payment for a 10.00 @19% article. The redeeming sale is inside Net
// (its total) but must contribute NOTHING to any band or to TaxNet — VAT
// was collected at issue; the "single-purpose redeemed" bucket is the
// reconciling delta.
func TestEODTaxBands_SinglePurposeRedemptionDay(t *testing.T) {
	d := etbOpenDB(t, "eod-bands-sp-redeem.db")
	etbItem(t, d, "itm-cake", 1000)

	day := etbCompleteSale(t, d, pos.SaleInput{
		SaleType: "sale", Currency: "EUR", TaxInclusive: true,
		VoucherIssues: []pos.VoucherIssueInput{{
			VoucherID: "SP-RED", Amount: money.FromMinor(1000),
			VoucherType: data.VoucherTypeSinglePurpose, TaxRateBP: 1900,
		}},
		Payments: []pos.PaymentInput{{MethodID: "cash", Amount: money.FromMinor(1000)}},
	})
	etbCompleteSale(t, d, pos.SaleInput{
		SaleType: "sale", Currency: "EUR", TaxInclusive: true, AllowNegativeInventory: true,
		Lines: []pos.SaleLineInput{{
			ItemID: "itm-cake", Name: "Cake", Qty: 1,
			UnitPrice: money.FromMinor(1000), TaxRateBasisPoints: 1900, LocationID: "loc_main",
		}},
		Payments: []pos.PaymentInput{{MethodID: "voucher", VoucherID: "SP-RED", Amount: money.FromMinor(1000)}},
	})
	// A control sale paid by cash on the same day, to prove the exclusion is
	// per-sale, not per-day.
	etbCompleteSale(t, d, pos.SaleInput{
		SaleType: "sale", Currency: "EUR", TaxInclusive: true, AllowNegativeInventory: true,
		Lines: []pos.SaleLineInput{{
			ItemID: "itm-cake", Name: "Cake", Qty: 1,
			UnitPrice: money.FromMinor(1000), TaxRateBasisPoints: 1900, LocationID: "loc_main",
		}},
		Payments: []pos.PaymentInput{{MethodID: "cash", Amount: money.FromMinor(1000)}},
	})

	rep := etbEndOfDayWithMethods(t, data.NewPOSRepo(d.DB), day)
	if rep.TaxNet != 320 || rep.Net != 3000 {
		t.Fatalf("engine totals moved: TaxNet=%d Net=%d, want 320/3000 (issue 160 + control 160; the redemption adds no VAT)", rep.TaxNet, rep.Net)
	}
	if rep.VouchersSinglePurposeRedeemed != 1000 || rep.VouchersSinglePurposeRedeemedCount != 1 {
		t.Fatalf("single-purpose redeemed = %d/%d, want 1/1000", rep.VouchersSinglePurposeRedeemedCount, rep.VouchersSinglePurposeRedeemed)
	}
	if rep.VouchersRedeemed != 0 || rep.VouchersIssued != 0 {
		t.Fatalf("multi-purpose buckets must stay empty: issued %d redeemed %d", rep.VouchersIssued, rep.VouchersRedeemed)
	}
	if want := (data.TaxBand{RateBP: 1900, Net: 1680, Tax: 320, Gross: 2000}); len(rep.TaxBands) != 1 || rep.TaxBands[0] != want {
		t.Fatalf("bands = %+v, want [%+v] (issue + control; the redeeming sale contributes nothing)", rep.TaxBands, want)
	}
	assertEODTaxBandIdentities(t, rep)
	assertMethodBandsReconcile(t, rep)
	// The discriminating check: an exact-match redemption mirrors its own
	// issue's figures, so the band totals above would ALSO come out right
	// if the implementation wrongly excluded the issue and included the
	// redemption. The payment methods tell the two apart — the issue was
	// paid in cash, the redemption by voucher — so the whole band must sit
	// in the cash column and no voucher cell may exist at all.
	var cash data.TaxBand
	for _, c := range rep.MethodTaxBands {
		if c.Method == "voucher" {
			t.Fatalf("method cross-tab has a voucher cell %+v — the single-purpose redemption must not be apportioned into any band", c)
		}
		if c.Method == "cash" && c.RateBP == 1900 {
			cash = data.TaxBand{RateBP: 1900, Net: c.Net, Tax: c.Tax, Gross: c.Gross}
		}
	}
	if cash != rep.TaxBands[0] {
		t.Fatalf("cash column at 19%% = %+v, want the whole band %+v (issue sale + control sale, both cash)", cash, rep.TaxBands[0])
	}

	// The Tax tab's rolling-window report bands the same sales through the
	// same helper, so it must agree.
	from, to := etbWindowAround(t, day)
	bands, err := computeTaxSummary(context.Background(), data.NewPOSRepo(d.DB), from, to)
	if err != nil {
		t.Fatalf("computeTaxSummary: %v", err)
	}
	if len(bands) != 1 || bands[0] != rep.TaxBands[0] {
		t.Fatalf("Tax tab bands = %+v, Z-report bands = %+v — must agree", bands, rep.TaxBands)
	}
}

// The printed Z-report's GUTSCHEINE section names the single-purpose
// flows on their own lines — informational, never summed into Issued/
// Redeemed — and prints the section when ONLY single-purpose flows exist.
func TestBuildEODDoc_VoucherSection_SinglePurposeLines(t *testing.T) {
	rep := data.EODReport{
		Day: "2026-09-19", GeneratedAt: "2026-09-19T21:30:00Z",
		SalesCount: 2, Gross: 2190, Net: 2190, TaxNet: 350,
		VouchersSinglePurposeIssuedCount: 1, VouchersSinglePurposeIssued: 1190,
		VouchersSinglePurposeRedeemedCount: 1, VouchersSinglePurposeRedeemed: 1000,
	}
	out := string(print.Render(buildEODDoc(rep, "Test Shop", "utf8", eodArticlePrintAll, 0)))
	for _, want := range []string{
		"GUTSCHEINE",
		"SP issued (1)", "£11.90",
		"SP redeemed (1)", "£10.00",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Z-report voucher section missing %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "Issued (0)") {
		t.Errorf("a day with only single-purpose flows must not print the multi-purpose Issued/Redeemed pair as zeros\n%s", out)
	}
}
