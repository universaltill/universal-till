package pages

import (
	"context"
	"time"

	"github.com/universaltill/universal-till/internal/data"
)

// The Reports "Tax" tab's VAT-by-rate table (ut-docs#1115). Before this
// file, the tab was backed by POSRepo.TaxSummary, a raw-SQL aggregation
// directly over sale_lines — the same shape ut-docs#1003 already replaced
// for the day-close Z-report, because it can't see sale-level amounts that
// have no sale_lines row at all (a whole-sale discount, a service charge).
// ut-docs#1035 then widened the gap further: sales.tax_total (what the
// Z-report's totals are built from) started correctly discounting an
// inclusive-priced sale's tax, while TaxSummary's independent SQL sum still
// didn't — so the Tax tab and the Z-report could show two different VAT
// figures for the same sales.
//
// computeTaxSummary closes that by going through the exact same path
// eod_tax_bands.go's computeEODTaxBands already does: fetch raw sales+lines
// from internal/data, then band them with pos.VATBandsForSale via
// computeEODTaxBandsFromSales. internal/data can't import internal/pos
// directly (see eod_tax_bands.go's own note), which is why this lives here
// rather than as a POSRepo method — same reason TaxSummary's replacement,
// SalesForTaxWindow, is a plain data fetch and the banding math is not.

// computeTaxSummary aggregates per-sale VAT bands over an arbitrary
// [from, to) window — the Tax tab's own rolling-window report, as opposed
// to the Z-report's single calendar day. Reuses computeEODTaxBandsFromSales
// unchanged: same per-sale pos.VATBandsForSale call, same return sign-flip,
// same per-rate merge and ascending sort — so the two can never disagree
// over the SAME set of sales. The window itself can still pick a different
// sale set than a Z-report's, though: see SalesForTaxWindow's own doc
// comment on the business-day-shift caveat.
func computeTaxSummary(ctx context.Context, repo *data.POSRepo, from, to time.Time) ([]data.TaxBand, error) {
	sales, err := repo.SalesForTaxWindow(ctx, from, to)
	if err != nil {
		return nil, err
	}
	return computeEODTaxBandsFromSales(sales), nil
}
