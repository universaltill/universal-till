package pos

import (
	"sort"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/money"
)

// EODTaxBandsFromSales is the day-close per-VAT-rate aggregation
// (ut-docs#1003): one VATBandsForSale call per completed sale,
// sign-flipped for returns, merged per rate and ordered ascending. It
// moved here from internal/pages (ut-docs#2535) so a second consumer —
// internal/cloudsync's per-till sales-aggregate upload — bands a till's
// sales with exactly the Z-report's math instead of a parallel copy
// (internal/cloudsync cannot import internal/pages: pages imports
// cloudsync). internal/pages' computeEODTaxBandsFromSales delegates here,
// so the Z-report, the Tax tab and the cloud upload can never disagree.
// See internal/pages/eod_tax_bands.go for the identities this upholds
// (sum(band.Tax) == TaxNet; sum(band.Gross) == Net − multi-purpose voucher
// issues). Returns nil when there are no bands.
func EODTaxBandsFromSales(sales []data.EODTaxBandSale) []data.TaxBand {
	agg := map[int]*data.TaxBand{}
	for _, s := range sales {
		inclusive := InferTaxInclusive(s.Subtotal, s.DiscountTotal, s.TaxTotal, s.Total, s.ServiceCharge, s.VoucherIssueTotal)
		lines := EODVATLinesForSale(s, inclusive)
		sign := int64(1)
		if s.SaleType == "return" {
			sign = -1
		}
		for _, b := range VATBandsForSale(lines, s.DiscountTotal, inclusive, s.ServiceCharge, s.ServiceChargeTaxBasisBP) {
			e, ok := agg[b.RateBP]
			if !ok {
				e = &data.TaxBand{RateBP: b.RateBP}
				agg[b.RateBP] = e
			}
			e.Net += sign * b.Net
			e.Tax += sign * b.Tax
			e.Gross += sign * b.Gross
		}
	}
	out := make([]data.TaxBand, 0, len(agg))
	for _, b := range agg {
		out = append(out, *b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RateBP < out[j].RateBP })
	if len(out) == 0 {
		return nil
	}
	return out
}

// EODVATLinesForSale is the ONE adapter from a data.EODTaxBandSale to the
// VATLine slice VATBandsForSale bands. It re-adds the sale's SINGLE-PURPOSE
// voucher issues (ut-docs#1037) as lines: each was taxed at issue by
// computeSaleTotals — into the persisted subtotal/tax_total — but has no
// sale_lines row, so banding sale_lines alone would leave sum(band.Tax)
// short of TaxNet by exactly that tax. Multi-purpose issues are not in the
// read at all (see data.EODTaxBandSale.SinglePurposeVoucherIssues).
func EODVATLinesForSale(s data.EODTaxBandSale, inclusive bool) []VATLine {
	lines := make([]VATLine, 0, len(s.Lines)+len(s.SinglePurposeVoucherIssues))
	for _, l := range s.Lines {
		lines = append(lines, VATLine{RateBP: l.RateBP, LineTotal: l.LineTotal, TaxAmount: l.TaxAmount})
	}
	for _, vi := range s.SinglePurposeVoucherIssues {
		lines = append(lines, SinglePurposeVoucherVATLine(vi.RateBP, vi.Amount, inclusive))
	}
	return lines
}

// SinglePurposeVoucherVATLine is a single-purpose voucher issue as the
// VATLine its issuing sale taxed it as (ut-docs#1037): amount is in the
// sale's own pricing mode (gross inclusive / net exclusive, exactly what
// computeSaleTotals passed), so the returned gross+tax match the engine's.
func SinglePurposeVoucherVATLine(rateBP int, amount int64, inclusive bool) VATLine {
	tax, gross := ComputeTaxBasisPoints(money.FromMinor(amount), rateBP, inclusive)
	return VATLine{RateBP: rateBP, LineTotal: gross.Minor(), TaxAmount: tax.Minor()}
}
