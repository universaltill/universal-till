package pos

import (
	"sort"

	"github.com/universaltill/universal-till/internal/money"
)

// This file is the single shared per-sale VAT banding used by BOTH the
// invoice/credit-note VAT table (internal/pages/invoice_page.go's
// vatBreakdown) and the day-close Z-report's per-rate breakdown
// (internal/pages/eod_tax_bands.go, ut-docs#1003). The logic was extracted
// verbatim from vatBreakdown — which was the reference implementation —
// because the day-close code path needed it too and internal/data cannot
// import internal/pos (internal/pos already imports internal/data), so the
// math had to live here where both callers can reach it. Do not fork it
// back into a caller: ADR-0061 requires every VAT-band computation to run
// the service charge through the SAME ApportionServiceChargeTax call, so
// nothing can declare different VAT on a charge than the sale collected.

// VATLine is one persisted sale line's input to VATBandsForSale, in the
// sale's own recorded figures (minor units):
//   - RateBP: the line's recorded tax rate (sale_lines.tax_rate_bp) — the
//     sale's own tax signature, never today's settings.
//   - LineTotal: the line's total_after_tax (data.SaleDetailLine.LineTotal)
//     — tax-inclusive under either pricing mode.
//   - TaxAmount: the line's recorded tax (sale_lines.tax_amount).
type VATLine struct {
	RateBP    int
	LineTotal int64
	TaxAmount int64
}

// VATBand is one per-rate row of a sale's VAT breakdown. Net+Tax == Gross
// for every band, and the bands' Gross sums to what the customer actually
// paid for the sale (its persisted total).
type VATBand struct {
	RateBP int
	Net    int64
	Tax    int64
	Gross  int64
}

// VATBandsForSale aggregates a sale's lines by their RECORDED tax rate. A
// whole-sale discount (sale_discounts — never folded into any line) is
// prorated across the bands by gross share, largest-remainder so the pennies
// land on the highest band and the bands still sum to what the customer
// actually paid; and a service charge — which since ADR-0061 carries its own
// VAT — is apportioned into the bands through ApportionServiceChargeTax, the
// SAME shared function the tender path and fiscal.sign.ask use.
//
// Inclusive vs exclusive proration mirrors the engine (computeSaleTotals):
// inclusive takes the discount off the gross and re-derives net/tax at the
// band's rate (total = subtotal − d); exclusive discounts the NET base and
// keeps line tax as computed (total = subtotal − d + tax).
func VATBandsForSale(lines []VATLine, discountTotal int64, taxInclusive bool, serviceCharge int64, serviceChargeTaxBasisBP int) []VATBand {
	byRate := map[int]*VATBand{}
	var grossSum int64
	for _, l := range lines {
		b, ok := byRate[l.RateBP]
		if !ok {
			b = &VATBand{RateBP: l.RateBP}
			byRate[l.RateBP] = b
		}
		b.Gross += l.LineTotal
		b.Tax += l.TaxAmount
		b.Net += l.LineTotal - l.TaxAmount
		grossSum += l.LineTotal
	}
	out := make([]VATBand, 0, len(byRate))
	for _, b := range byRate {
		out = append(out, *b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RateBP < out[j].RateBP })

	if d := discountTotal; d > 0 && grossSum > 0 {
		remaining := d
		for i := range out {
			share := d * out[i].Gross / grossSum
			if i == len(out)-1 {
				share = remaining // largest-remainder: the pennies land here
			}
			remaining -= share
			if taxInclusive {
				// Discount comes off the gross; re-derive net/tax at the
				// band's rate (mirrors the engine: total = subtotal − d).
				// NOTE (ut-docs#1035): this re-derivation TRUNCATES, while
				// ComputeTaxBasisPoints (the undiscounted per-line path both
				// callers otherwise share) rounds half-up -- so a discounted
				// inclusive band's tax can differ by up to 1 minor unit from
				// what the same figures would give undiscounted. Consistent
				// across every caller of this function either way, and
				// truncation is biased toward declaring slightly MORE tax,
				// never less -- the safe direction -- so this is a known
				// rounding-rule difference, not a defect.
				out[i].Gross -= share
				out[i].Net = out[i].Gross * 10000 / (10000 + int64(out[i].RateBP))
				out[i].Tax = out[i].Gross - out[i].Net
			} else {
				// Exclusive engine discounts the NET base and keeps line
				// tax as computed (total = subtotal − d + tax).
				out[i].Net -= share
				out[i].Gross = out[i].Net + out[i].Tax
			}
		}
	}
	// ADR-0061 Decision 2: the service charge carries VAT of its own,
	// apportioned across the sale's own rate bands (or taxed at the flat
	// basis the originating till's country plugin fixed, which rides the
	// sale row so a re-issued invoice matches the original). Folded in
	// here rather than left as an untaxed lump, so the VAT table declares
	// the charge's tax and the bands still sum to what the customer paid.
	if serviceCharge > 0 {
		chargeLines := make([]ChargeTaxLine, 0, len(lines))
		for _, l := range lines {
			// The band weights want each line's value in the sale's OWN
			// pricing mode -- gross when inclusive (the shared function
			// derives the true net itself), net when exclusive.
			net := l.LineTotal
			if !taxInclusive {
				net -= l.TaxAmount
			}
			chargeLines = append(chargeLines, ChargeTaxLine{RateBP: l.RateBP, Net: money.FromMinor(net)})
		}
		for _, b := range ApportionServiceChargeTax(money.FromMinor(serviceCharge), chargeLines, taxInclusive, serviceChargeTaxBasisBP) {
			idx := -1
			for i := range out {
				if out[i].RateBP == b.RateBP {
					idx = i
					break
				}
			}
			if idx < 0 {
				out = append(out, VATBand{RateBP: b.RateBP})
				idx = len(out) - 1
			}
			// b.Amount is in the sale's pricing mode, same as the charge:
			// inclusive -> it already contains b.Tax; exclusive -> the tax
			// rides on top.
			if taxInclusive {
				out[idx].Gross += b.Amount.Minor()
				out[idx].Net += b.Amount.Minor() - b.Tax.Minor()
			} else {
				out[idx].Net += b.Amount.Minor()
				out[idx].Gross += b.Amount.Minor() + b.Tax.Minor()
			}
			out[idx].Tax += b.Tax.Minor()
		}
		sort.Slice(out, func(i, j int) bool { return out[i].RateBP < out[j].RateBP })
	}
	return out
}

// InferTaxInclusive infers a persisted sale's pricing mode from its own
// header arithmetic (settings may have changed since the sale happened):
// inclusive keeps total = subtotal − discount + service charge + voucher
// issues; exclusive adds tax on top. All values are the sale row's
// persisted minor units.
//
// A service charge is added to the total in BOTH modes (inclusive keeps
// its tax inside the amount, exclusive adds it on top with the lines'),
// so it has to be part of the comparison or an inclusive sale carrying one
// is misread as exclusive — which then mis-derives the whole sale on
// every path that asks: the invoice VAT breakdown, the refund math, the
// day-close VAT bands, and (since ADR-0061 taxes the charge by pricing
// mode) a journal replay's recomputed totals. The same holds for a
// voucher issue's face value (ut-docs#1008 review, blocker F1): it too is
// folded into total in both modes with no subtotal/taxTotal counterpart
// (a 0% liability, sales.voucher_issue_total — migration 069), so leaving
// it out of the identity made every inclusive sale that also issued a
// voucher read as exclusive, double-charging VAT on its refunds. This is
// the single shared inference — internal/pages' saleIsTaxInclusive
// delegates here.
func InferTaxInclusive(subtotal, discountTotal, taxTotal, total, serviceCharge, voucherIssueTotal int64) bool {
	if taxTotal == 0 {
		return false // both modes agree; exclusive math is the identity
	}
	return total == subtotal-discountTotal+serviceCharge+voucherIssueTotal
}
