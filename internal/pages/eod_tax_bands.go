package pages

import (
	"context"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pos"
)

// Day-close per-VAT-rate breakdown (ut-docs#1003). This lives in pages,
// not internal/data, deliberately: the correct per-sale banding needs
// internal/pos (discount proration + ADR-0061's shared
// ApportionServiceChargeTax), and internal/data cannot import internal/pos
// (internal/pos already imports internal/data). A pure SQL aggregation
// over sale_lines was the first implementation and silently missed two
// sale-level amounts that have no sale_lines row — the service charge's
// tax and the whole-sale discount — so any sale carrying either broke the
// Z-report's own identities, under-declaring VAT collected on service
// charges. Those identities are: sum(band.Tax) == TaxNet always, and
// sum(band.Gross) == Net on any day WITHOUT voucher issues — a MULTI-PURPOSE
// voucher issue's face value (ut-docs#1008) is inside Net (it is in
// sales.total) but deliberately in NO band (a 0% liability, not a taxable
// supply), so on a voucher day sum(band.Gross) == Net − vouchers issued, and
// the GUTSCHEINE section supplies exactly that reconciling delta. A
// SINGLE-PURPOSE issue (ut-docs#1037) is the opposite case: taxed at issue,
// so it IS in a band (eodVATLinesForSale re-adds it from the vouchers row,
// since it has no sale_lines row) and is NOT in the GUTSCHEINE figure
// (data.VouchersIssuedRedeemedForRange excludes it) — the identity holds
// unchanged with both kinds on one day. This is the
// SAME shared pos.VATBandsForSale the invoice's VAT table uses, so the
// day-close can never disagree with the invoices issued for its sales.
//
// sum(band.Tax) == TaxNet holds for every sale persisted by a build
// carrying ut-docs#1035's fix onward. A sale row written by an OLDER build
// can still violate it for exactly one shape (inclusive pricing + a
// whole-sale discount) — pos.computeSaleTotals's own doc comment carries
// the full explanation. ut-docs#1114 is the product-owner decision on
// what to do about those historical rows: for now, a documented known-gap
// (no evidence any wrong figure ever reached a filed VAT return), not a
// migration — see that ticket before writing one. Not this function's bug
// to fix; noted here because this is where the identity it breaks lives.

// computeEODTaxBands aggregates per-sale VAT bands over the SAME
// local-calendar-day window dateRangeSummary uses (ut-docs#869), one
// pos.VATBandsForSale call per completed sale, sign-flipped for returns
// (mirroring the report's other figures), merged per rate and ordered
// ascending (0%, 7%, 19% — the card's reference layout).
//
// Test-only-reachable, kept deliberately (ut-docs#1566 — on
// scripts/ci/deadcode-baseline.txt, not a deletion candidate): no
// production path calls this read+aggregate form today. ut-docs#1004
// first moved both production call sites onto attachEODBands, a single
// shared sales read feeding both TaxBands and MethodTaxBands; commit
// 8548a3af (ADR-0066, 2026-09-04) then moved generateEOD off
// attachEODBands onto its own inline SalesForTaxBandsInstant read, so
// today generateEOD reads SalesForTaxBandsInstant itself and calls
// computeEODTaxBandsFromSales directly, while only the range-export
// handler still goes through attachEODBands — both so TaxBands and
// MethodTaxBands come from ONE sales read. This standalone form remains
// the single-breakdown
// equivalent this package's own tests exercise (eod_tax_bands_test.go,
// tax_summary_test.go, eod_method_tax_bands_test.go — via
// attachEODTaxBands), including the test that proves attachEODBands
// matches the two separate calls.
func computeEODTaxBands(ctx context.Context, repo *data.POSRepo, from, to string) ([]data.TaxBand, error) {
	sales, err := repo.SalesForTaxBands(ctx, from, to)
	if err != nil {
		return nil, err
	}
	return computeEODTaxBandsFromSales(sales), nil
}

// computeEODTaxBandsFromSales is computeEODTaxBands' pure aggregation step,
// split out (ut-docs#1004 review finding) so attachEODBands can compute
// TaxBands and MethodTaxBands from the SAME SalesForTaxBands read instead
// of two independent reads a moment apart — two reads could let a sale
// completing in between appear in one breakdown and not the other,
// silently breaking the row-sum reconciliation identity this package's own
// tests otherwise treat as exact "always".
func computeEODTaxBandsFromSales(sales []data.EODTaxBandSale) []data.TaxBand {
	// The aggregation itself lives in internal/pos (ut-docs#2535) so
	// internal/cloudsync's per-till sales-aggregate upload bands with the
	// identical math; this wrapper keeps every pages caller unchanged.
	return pos.EODTaxBandsFromSales(sales)
}

// eodVATLinesForSale is the ONE adapter from a data.EODTaxBandSale to the
// pos.VATLine slice VATBandsForSale bands — shared by computeEODTaxBandsFromSales
// and computeEODMethodTaxBandsFromSales (and, through the former, the Tax
// tab's computeTaxSummary) so every breakdown sees the same lines. It
// re-adds the sale's SINGLE-PURPOSE voucher issues (ut-docs#1037) as lines:
// each was taxed at issue by pos.computeSaleTotals — into the persisted
// subtotal/tax_total — but has no sale_lines row, so banding sale_lines
// alone would leave sum(band.Tax) short of TaxNet by exactly that tax. The
// same ComputeTaxBasisPoints call, at the rate the voucher row recorded and
// under the sale's own inferred pricing mode, re-derives the identical
// figure the engine persisted. Multi-purpose issues are not in the read at
// all (see data.EODTaxBandSale.SinglePurposeVoucherIssues).
func eodVATLinesForSale(s data.EODTaxBandSale, inclusive bool) []pos.VATLine {
	return pos.EODVATLinesForSale(s, inclusive)
}

// singlePurposeVoucherVATLine is a single-purpose voucher issue as the
// pos.VATLine its issuing sale taxed it as (ut-docs#1037): amount is in the
// sale's own pricing mode (gross inclusive / net exclusive, exactly what
// computeSaleTotals passed), so the returned gross+tax match the engine's.
// Shared by the day-close adapter above and the invoice's vatBreakdown.
func singlePurposeVoucherVATLine(rateBP int, amount int64, inclusive bool) pos.VATLine {
	return pos.SinglePurposeVoucherVATLine(rateBP, amount, inclusive)
}

// attachEODTaxBands fills rep.TaxBands for a report EndOfDay/EndOfDayRange
// just produced, reading the window off the report itself (Day for the
// single-day form, From/To for a range). An error is an error — a Z-report
// missing its VAT table is not a Z-report, so callers must fail rather
// than archive/print/export one without it.
//
// Test-only-reachable today (ut-docs#1004 then commit 8548a3af moved both
// production call sites off this function; the range export now uses
// attachEODBands, generateEOD reads inline); kept deliberately
// (ut-docs#1566) — see computeEODTaxBands' doc comment.
func attachEODTaxBands(ctx context.Context, repo *data.POSRepo, rep *data.EODReport) error {
	from, to := rep.Day, rep.Day
	if rep.Day == "" {
		from, to = rep.From, rep.To
	}
	bands, err := computeEODTaxBands(ctx, repo, from, to)
	if err != nil {
		return err
	}
	rep.TaxBands = bands
	return nil
}
