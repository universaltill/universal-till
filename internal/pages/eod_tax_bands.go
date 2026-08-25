package pages

import (
	"context"
	"sort"

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
// sum(band.Gross) == Net on any day WITHOUT voucher issues — a voucher
// issue's face value (ut-docs#1008) is inside Net (it is in sales.total)
// but deliberately in NO band (a 0% liability, not a taxable supply), so
// on a voucher day sum(band.Gross) == Net − vouchers issued, and the
// GUTSCHEINE section supplies exactly that reconciling delta. This is the
// SAME shared pos.VATBandsForSale the invoice's VAT table uses, so the
// day-close can never disagree with the invoices issued for its sales.

// computeEODTaxBands aggregates per-sale VAT bands over the SAME
// local-calendar-day window dateRangeSummary uses (ut-docs#869), one
// pos.VATBandsForSale call per completed sale, sign-flipped for returns
// (mirroring the report's other figures), merged per rate and ordered
// ascending (0%, 7%, 19% — the card's reference layout).
func computeEODTaxBands(ctx context.Context, repo *data.POSRepo, from, to string) ([]data.TaxBand, error) {
	sales, err := repo.SalesForTaxBands(ctx, from, to)
	if err != nil {
		return nil, err
	}
	agg := map[int]*data.TaxBand{}
	for _, s := range sales {
		lines := make([]pos.VATLine, 0, len(s.Lines))
		for _, l := range s.Lines {
			lines = append(lines, pos.VATLine{RateBP: l.RateBP, LineTotal: l.LineTotal, TaxAmount: l.TaxAmount})
		}
		inclusive := pos.InferTaxInclusive(s.Subtotal, s.DiscountTotal, s.TaxTotal, s.Total, s.ServiceCharge, s.VoucherIssueTotal)
		sign := int64(1)
		if s.SaleType == "return" {
			sign = -1
		}
		for _, b := range pos.VATBandsForSale(lines, s.DiscountTotal, inclusive, s.ServiceCharge, s.ServiceChargeTaxBasisBP) {
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
		return nil, nil
	}
	return out, nil
}

// attachEODTaxBands fills rep.TaxBands for a report EndOfDay/EndOfDayRange
// just produced, reading the window off the report itself (Day for the
// single-day form, From/To for a range). An error is an error — a Z-report
// missing its VAT table is not a Z-report, so callers must fail rather
// than archive/print/export one without it.
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
