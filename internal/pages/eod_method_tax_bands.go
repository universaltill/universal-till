package pages

import (
	"context"
	"sort"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pos"
)

// Day-close payment-method x VAT-rate cross-tab (ut-docs#1004) — the cell
// grid the DATEV/accounting export posting batch is generated from (debit
// account = payment method, credit account = VAT rate). Like the per-rate
// breakdown in eod_tax_bands.go — and for the same reason — this lives in
// pages, not internal/data: the per-sale banding needs internal/pos
// (discount proration + ADR-0061's shared ApportionServiceChargeTax), and
// internal/data cannot import internal/pos.
//
// The data model has no line-level payment attribution (a payment covers
// the sale, not a line), so a split-tender sale's bands are APPORTIONED
// across its payments by tendered-revenue share, floor + last-payment
// remainder: every payment but the last gets
// floor(bandAmount * payment / totalTendered), and the last payment (in
// the stable per-sale query order, ORDER BY method_id) takes the exact
// remainder. That rule is drift-free by construction — the parts always
// sum back to EXACTLY the sale's own band amount, so summing the cells
// per rate reproduces EODReport.TaxBands to the minor unit — PROVIDED both
// are computed from the same SalesForTaxBands snapshot, which is exactly
// what attachEODBands (below) guarantees for both of this package's real
// callers; see its own doc comment for what can drift if that precondition
// doesn't hold (e.g. two independent reads a moment apart).
//
// Net and Tax are apportioned independently; Gross is then DERIVED as
// Net + Tax per aggregated cell, never apportioned as a third
// independent split. This is load-bearing: it is what guarantees every
// cell satisfies Gross == Net + Tax by construction (mirroring
// TaxBand.Gross == Net + Tax) — three independent floor splits could
// disagree by a minor unit.
//
// Tips caveat (the reconciliation rule, pinned by
// TestEndOfDay_MethodTaxBands_TipsExcluded): a tip carries no VAT rate,
// so SalesForTaxBands' payment query excludes tip_amount from each
// payment's tendered-revenue share. A method's cross-tab column total
// therefore reconciles to its tendered REVENUE — EODMethod.In minus that
// method's EODTip.Amount (minus EODMethod.Out where it took refunds) —
// NOT to EODMethod.In directly, whenever the method carries tips. This is
// analogous to the voucher-issue reconciling delta documented in
// eod_tax_bands.go: a deliberate, explained difference, not drift. (On a
// split-tender sale whose shares don't divide exactly, the flooring can
// also shift a minor unit between that sale's methods — never lost
// overall, the per-rate identity still holds exactly.)

// apportionAmount splits total across shares by floor(total*share/
// totalShares), with the LAST share taking the exact remainder, so the
// returned parts always sum to exactly total. Callers pass unsigned
// magnitudes (band amounts and tendered shares); the sale's sign is
// applied by the caller when aggregating. totalShares must be nonzero
// (callers skip zero-tendered sales).
func apportionAmount(total int64, shares []int64, totalShares int64) []int64 {
	out := make([]int64, len(shares))
	var allocated int64
	for i, sh := range shares {
		if i == len(shares)-1 {
			out[i] = total - allocated
		} else {
			out[i] = total * sh / totalShares
			allocated += out[i]
		}
	}
	return out
}

// computeEODMethodTaxBands aggregates the (method, rate) cross-tab over
// the same local-calendar-day window as computeEODTaxBands, running each
// sale through the SAME shared pos.VATBandsForSale (same inputs, same
// pos.InferTaxInclusive inference), apportioning each band across the
// sale's payments as described in the file doc comment, sign-flipping
// returns, and merging per (method, rate).
//
// This calls SalesForTaxBands itself — a standalone read, independent of
// any TaxBands computation — for callers/tests that only need the
// cross-tab. The two production call sites in eod_api.go do NOT use this
// directly; they call attachEODBands, which reads sales ONCE and feeds
// both computeEODTaxBandsFromSales and computeEODMethodTaxBandsFromSales
// from that single snapshot (see attachEODBands' doc comment for why).
//
// Output order is deterministic: ascending by Method, then by RateBP —
// grouping a method's rates together, the way the posting batch reads,
// and the way the printed "BY METHOD & VAT RATE" section groups them.
// This exact order IS asserted by this package's own golden tests (not
// merely "stable for printing" — a reordering would need those tests
// updated deliberately, not silently pass).
func computeEODMethodTaxBands(ctx context.Context, repo *data.POSRepo, from, to string) ([]data.MethodTaxBand, error) {
	sales, err := repo.SalesForTaxBands(ctx, from, to)
	if err != nil {
		return nil, err
	}
	return computeEODMethodTaxBandsFromSales(sales), nil
}

// computeEODMethodTaxBandsFromSales is computeEODMethodTaxBands' pure
// aggregation step — see attachEODBands' doc comment for why this is split
// out from the SalesForTaxBands read.
func computeEODMethodTaxBandsFromSales(sales []data.EODTaxBandSale) []data.MethodTaxBand {
	agg := map[string]map[int]*data.MethodTaxBand{}
	for _, s := range sales {
		var totalTendered int64
		shares := make([]int64, len(s.Payments))
		for i, p := range s.Payments {
			shares[i] = p.Amount
			totalTendered += p.Amount
		}
		if totalTendered == 0 {
			// pos.CompleteSale always records >=1 payment for a completed
			// sale, but that alone doesn't guarantee totalTendered > 0: a
			// 100%-discounted sale (amount == change_given on its one
			// payment, or a payment set that nets to zero) satisfies
			// CompleteSale's "at least one payment" check yet has nothing
			// to apportion by. Skipping is safe (that sale's own bands are
			// zero too), but it silently drops the sale from
			// MethodTaxBands while TaxBands still includes it — logged so
			// a genuinely hand-broken row is visible rather than quietly
			// unbalancing the reconciliation identity (ut-docs#1004 review
			// finding).
			logging.L().Warnf("eod method tax bands: sale %s has zero total tendered, skipped from cross-tab", s.ID)
			continue
		}
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
			// Apportion the band's unsigned magnitudes; the sale's sign is
			// applied only when adding into the aggregate (same convention
			// computeEODTaxBands uses).
			nets := apportionAmount(b.Net, shares, totalTendered)
			taxes := apportionAmount(b.Tax, shares, totalTendered)
			for i, p := range s.Payments {
				rates, ok := agg[p.Method]
				if !ok {
					rates = map[int]*data.MethodTaxBand{}
					agg[p.Method] = rates
				}
				cell, ok := rates[b.RateBP]
				if !ok {
					cell = &data.MethodTaxBand{Method: p.Method, RateBP: b.RateBP}
					rates[b.RateBP] = cell
				}
				cell.Net += sign * nets[i]
				cell.Tax += sign * taxes[i]
			}
		}
	}
	var out []data.MethodTaxBand
	for _, rates := range agg {
		for _, cell := range rates {
			// Gross derived here, after all aggregation — see the file doc
			// comment for why it is never independently apportioned.
			cell.Gross = cell.Net + cell.Tax
			out = append(out, *cell)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Method != out[j].Method {
			return out[i].Method < out[j].Method
		}
		return out[i].RateBP < out[j].RateBP
	})
	// out is already nil when empty (declared with var, never make()'d) —
	// unlike computeEODTaxBandsFromSales, which uses make() and so needs
	// its own explicit nil-normalization, no such step is needed here.
	return out
}

// attachEODMethodTaxBands fills rep.MethodTaxBands for a report EndOfDay/
// EndOfDayRange just produced, reading the window off the report itself
// (Day for the single-day form, From/To for a range) — the same contract
// as attachEODTaxBands: an error is an error, callers must fail rather
// than archive/print/export a report without its cross-tab.
func attachEODMethodTaxBands(ctx context.Context, repo *data.POSRepo, rep *data.EODReport) error {
	from, to := rep.Day, rep.Day
	if rep.Day == "" {
		from, to = rep.From, rep.To
	}
	cells, err := computeEODMethodTaxBands(ctx, repo, from, to)
	if err != nil {
		return err
	}
	rep.MethodTaxBands = cells
	return nil
}

// attachEODBands fills BOTH rep.TaxBands and rep.MethodTaxBands from a
// SINGLE SalesForTaxBands read (ut-docs#1004 review finding). Before this,
// generateEOD and the range-export handler called attachEODTaxBands and
// attachEODMethodTaxBands back to back — two independent reads of the same
// window a moment apart. On the single-day scheduled path that's harmless
// (the window is a closed calendar day already in the past), but
// /api/reports/eod/range can run on demand while the shop is still
// trading: a sale completing in the gap between the two reads could then
// appear in one breakdown and not the other, silently breaking the
// row-sum reconciliation identity (sum of MethodTaxBands per rate ==
// the matching TaxBand) in exactly the artifact an accountant reconciles
// against. One shared snapshot removes the window entirely.
//
// This is the function generateEOD and the range handler actually call.
// attachEODTaxBands/attachEODMethodTaxBands remain as their own
// independent-read equivalents — still correct on their own, just not
// mutually consistent with each other if called back to back — for any
// caller (this package's own tests included) that only needs one
// breakdown and doesn't care about that consistency.
func attachEODBands(ctx context.Context, repo *data.POSRepo, rep *data.EODReport) error {
	from, to := rep.Day, rep.Day
	if rep.Day == "" {
		from, to = rep.From, rep.To
	}
	sales, err := repo.SalesForTaxBands(ctx, from, to)
	if err != nil {
		return err
	}
	rep.TaxBands = computeEODTaxBandsFromSales(sales)
	rep.MethodTaxBands = computeEODMethodTaxBandsFromSales(sales)
	return nil
}
