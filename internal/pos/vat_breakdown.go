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
//   - SinglePurposeVoucher: the line is a single-purpose voucher this sale
//     issues (ut-docs#1037, #4214), not goods. A basket discount applies to
//     the goods only, so VATBandsForSale never prorates it into this line
//     (ut-docs#4233). Set by SinglePurposeVoucherVATLine and by the two
//     engine paths that band a voucher being sold (computeSaleTotals,
//     computeTotals).
type VATLine struct {
	RateBP               int
	LineTotal            int64
	TaxAmount            int64
	SinglePurposeVoucher bool
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
// prorated across the bands by the gross share of their GOODS,
// largest-remainder so the pennies land on the highest band carrying goods
// — no band's share ever exceeding its goods, the excess rolling down to
// the next band (splitSaleDiscount, ut-docs#4277) — and the bands still
// sum to what the customer actually paid — a
// single-purpose voucher's share of a band is never discounted and keeps
// the tax it was issued with (ut-docs#4233); and a service charge — which since ADR-0061 carries its own
// VAT — is apportioned into the bands through ApportionServiceChargeTax, the
// SAME shared function the tender path and fiscal.sign.ask use.
//
// Inclusive vs exclusive proration mirrors the engine (computeSaleTotals):
// inclusive takes the discount off the gross and re-derives net/tax at the
// band's rate (total = subtotal − d); exclusive discounts the NET base and
// keeps line tax as computed (total = subtotal − d + tax).
func VATBandsForSale(lines []VATLine, discountTotal int64, taxInclusive bool, serviceCharge int64, serviceChargeTaxBasisBP int) []VATBand {
	// Each band remembers the single-purpose-voucher part of its figures
	// so the discount below reaches only its goods part (ut-docs#4233).
	type bandAcc struct {
		VATBand
		hasGoods                 bool
		voucherGross, voucherTax int64
	}
	byRate := map[int]*bandAcc{}
	var goodsGrossSum int64
	for _, l := range lines {
		b, ok := byRate[l.RateBP]
		if !ok {
			b = &bandAcc{VATBand: VATBand{RateBP: l.RateBP}}
			byRate[l.RateBP] = b
		}
		b.Gross += l.LineTotal
		b.Tax += l.TaxAmount
		b.Net += l.LineTotal - l.TaxAmount
		if l.SinglePurposeVoucher {
			b.voucherGross += l.LineTotal
			b.voucherTax += l.TaxAmount
			continue
		}
		b.hasGoods = true
		goodsGrossSum += l.LineTotal
	}
	acc := make([]*bandAcc, 0, len(byRate))
	for _, b := range byRate {
		acc = append(acc, b)
	}
	sort.Slice(acc, func(i, j int) bool { return acc[i].RateBP < acc[j].RateBP })

	if d := discountTotal; d > 0 {
		if goodsGrossSum <= 0 {
			// No goods to carry the discount (the basket caps a discount at
			// the goods only while a voucher is being sold, ut-docs#4214; a
			// tender with an explicit discount, or another direct
			// CompleteSale caller, can still get here):
			// prorate it across every band as before ut-docs#4233, so the
			// bands still sum to the sale's total.
			for _, b := range acc {
				b.hasGoods, b.voucherGross, b.voucherTax = true, 0, 0
				goodsGrossSum += b.Gross
			}
		}
		// Each band's share is capped at the goods it comes off and the
		// excess rolls down (ut-docs#4275, #4277; fiscal-sign-ask.md
		// § sale_discount step 3, as ut-plugin-tax-de signs it).
		weights := make([]int64, len(acc))
		caps := make([]int64, len(acc))
		for i, b := range acc {
			if !b.hasGoods {
				continue // a voucher-only band keeps its issue-time figures
			}
			weights[i] = b.Gross - b.voucherGross
			caps[i] = weights[i]
			if !taxInclusive {
				// Exclusive discounts the NET base, so that is what a
				// share must not exceed.
				caps[i] = b.Net - (b.voucherGross - b.voucherTax)
			}
		}
		shares := splitSaleDiscount(d, weights, caps)
		for i, b := range acc {
			if !b.hasGoods || goodsGrossSum <= 0 {
				continue
			}
			goodsGross := b.Gross - b.voucherGross
			share := shares[i]
			if taxInclusive {
				// Discount comes off the goods' gross; re-derive their
				// net/tax at the band's rate (mirrors the engine: total =
				// subtotal − d). The voucher part is added back unchanged.
				// NOTE (ut-docs#1035): this re-derivation TRUNCATES, while
				// ComputeTaxBasisPoints (the undiscounted per-line path both
				// callers otherwise share) rounds half-up -- so a discounted
				// inclusive band's tax can differ by up to 1 minor unit from
				// what the same figures would give undiscounted. Consistent
				// across every caller of this function either way, and
				// truncation is biased toward declaring slightly MORE tax,
				// never less -- the safe direction -- so this is a known
				// rounding-rule difference, not a defect.
				goodsGross -= share
				goodsNet := goodsGross * 10000 / (10000 + int64(b.RateBP))
				b.Gross = goodsGross + b.voucherGross
				b.Net = goodsNet + b.voucherGross - b.voucherTax
				b.Tax = b.Gross - b.Net
			} else {
				// Exclusive engine discounts the NET base and keeps line
				// tax as computed (total = subtotal − d + tax).
				b.Net -= share
				b.Gross = b.Net + b.Tax
			}
		}
	}
	out := make([]VATBand, 0, len(acc))
	for _, b := range acc {
		out = append(out, b.VATBand)
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

// splitSaleDiscount splits a sale discount d over the bands (sorted by
// ascending rate) in proportion to weights (each band's goods gross),
// largest-remainder (ut-docs#4277; fiscal-sign-ask.md § sale_discount
// steps 2–3):
//  1. every band with weight > 0 gets floor(d × weight / Σweight), capped
//     at its caps entry (the goods the discount comes off — gross
//     inclusive, net exclusive);
//  2. the minor units left over fill the bands from the highest rate down,
//     each up to its cap, so the pennies land on the highest band that
//     can still take them and none goes negative;
//  3. anything still left (a discount larger than every band's goods —
//     the basket caps a discount at the goods only while a voucher is
//     being sold, ut-docs#4214, so a fixed-amount discount larger than
//     the goods reaches here; tax and total are clamped to 0 afterwards)
//     goes on the highest band with weight > 0, so the shares always sum
//     to d.
//
// A band with weight ≤ 0 (no goods, or goods worth nothing) gets no share.
func splitSaleDiscount(d int64, weights, caps []int64) []int64 {
	shares := make([]int64, len(weights))
	var sum int64
	for _, w := range weights {
		if w > 0 {
			sum += w
		}
	}
	if sum <= 0 {
		return shares
	}
	remaining := d
	for i, w := range weights {
		if w <= 0 {
			continue
		}
		shares[i] = min(d*w/sum, max(caps[i], 0))
		remaining -= shares[i]
	}
	for i := len(shares) - 1; i >= 0 && remaining > 0; i-- {
		if weights[i] <= 0 {
			continue
		}
		if room := caps[i] - shares[i]; room > 0 {
			take := min(room, remaining)
			shares[i] += take
			remaining -= take
		}
	}
	if remaining != 0 {
		for i := len(shares) - 1; i >= 0; i-- {
			if weights[i] > 0 {
				shares[i] += remaining
				break
			}
		}
	}
	return shares
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
