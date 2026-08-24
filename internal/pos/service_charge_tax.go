package pos

import (
	"sort"

	"github.com/universaltill/universal-till/internal/money"
)

// ChargeTaxLine is one sale line's contribution to the rate-band weights
// ApportionServiceChargeTax splits a service charge across: the line's
// effective tax rate and its value after line discount (which still contains
// the line's tax when the sale is priced tax-inclusive — the function
// derives the true net itself).
type ChargeTaxLine struct {
	RateBP int
	Net    money.Money
}

// ServiceChargeTaxBand is one rate band's allocated share of a service
// charge and the tax on that share. Amount is in the sale's own pricing
// mode, same as the charge itself: exclusive → Tax goes on top of Amount;
// inclusive → Tax is contained within Amount.
type ServiceChargeTaxBand struct {
	RateBP int
	Amount money.Money
	Tax    money.Money
}

// ChargeTaxLinesFromSale derives the apportionment input from persisted-sale
// line inputs — the shared conversion computeSaleTotals and
// buildFiscalSignPayload (internal/pages/fiscal_sign_hook.go) both use, so
// the two can never disagree about what a line's net is.
func ChargeTaxLinesFromSale(lines []SaleLineInput) []ChargeTaxLine {
	out := make([]ChargeTaxLine, 0, len(lines))
	for _, l := range lines {
		net := AmountForQuantity(l.UnitPrice, l.Qty).Sub(l.LineDiscount)
		out = append(out, ChargeTaxLine{RateBP: l.TaxRateBasisPoints, Net: net})
	}
	return out
}

// ApportionServiceChargeTax is ADR-0060 Decision 2's single shared,
// pure apportionment: it splits a service charge across the sale's existing
// per-line tax rate bands BY NET LINE VALUE (not gross — deliberately
// different from invoice_page.go's vatBreakdown, which prorates a *discount*
// by *gross* share; only the largest-remainder rounding-fairness shape is
// shared), computing each band's tax at that band's own rate via
// ComputeTaxBasisPoints under the sale's pricing mode. Both call sites that
// used to mirror each other's totals math by discipline —
// internal/pos/sales.go's computeSaleTotals and internal/pages/
// fiscal_sign_hook.go's buildFiscalSignPayload — MUST call this one
// function, never re-derive it, so they cannot drift.
//
// taxBasisBP > 0 (a charge.policy.ask answer's service_charge_tax_basis_bp)
// taxes the whole charge at that one flat rate instead — a single band.
// taxBasisBP == 0 is the fail-closed default: with no installed plugin
// answering, the charge is STILL taxed at the sale's blended rates — the
// untaxed path is unreachable, by construction (no market researched for
// ut-docs#961 defaults to an untaxed service charge).
//
// Rounding: every band but the last gets the floor of its proportional
// share; the last band — bands are sorted ascending by rate, so the highest
// rate — absorbs the minor-unit remainder, so the shares always sum to
// exactly the charge. When the weights sum to zero (all-zero-value lines),
// that same rule lands the whole charge on the highest rate present — the
// conservative direction that can never under-declare. Returns nil for a
// non-positive charge or when there are no lines at all.
func ApportionServiceChargeTax(charge money.Money, lines []ChargeTaxLine, taxInclusive bool, taxBasisBP int) []ServiceChargeTaxBand {
	if !charge.IsPositive() {
		return nil
	}
	if taxBasisBP > 0 {
		tax, _ := ComputeTaxBasisPoints(charge, taxBasisBP, taxInclusive)
		return []ServiceChargeTaxBand{{RateBP: taxBasisBP, Amount: charge, Tax: tax}}
	}
	// Band weights: each rate's summed TRUE (tax-exclusive) net. Under
	// inclusive pricing a line's value contains its tax, so weighing by the
	// raw value would skew shares toward higher-rate bands.
	weightByRate := map[int]int64{}
	rates := make([]int, 0, len(lines))
	var totalWeight int64
	for _, l := range lines {
		net := l.Net
		if net.IsNegative() {
			net = 0
		}
		trueNet := net
		if taxInclusive {
			lineTax, _ := ComputeTaxBasisPoints(net, l.RateBP, true)
			trueNet = net.Sub(lineTax)
		}
		if _, seen := weightByRate[l.RateBP]; !seen {
			rates = append(rates, l.RateBP)
		}
		weightByRate[l.RateBP] += trueNet.Minor()
		totalWeight += trueNet.Minor()
	}
	if len(rates) == 0 {
		return nil
	}
	sort.Ints(rates)
	bands := make([]ServiceChargeTaxBand, 0, len(rates))
	remaining := charge
	for i, rate := range rates {
		var share money.Money
		if i == len(rates)-1 {
			share = remaining // largest-remainder: the pennies land on the highest band
		} else if totalWeight > 0 {
			// Floor, not half-up: floors can never over-allocate, so
			// `remaining` stays non-negative all the way to the last band.
			share = money.FromMinor(charge.Minor() * weightByRate[rate] / totalWeight)
		}
		remaining = remaining.Sub(share)
		tax, _ := ComputeTaxBasisPoints(share, rate, taxInclusive)
		bands = append(bands, ServiceChargeTaxBand{RateBP: rate, Amount: share, Tax: tax})
	}
	return bands
}

// ServiceChargeTax is the summed tax over ApportionServiceChargeTax's bands
// — the convenience form the totals paths use when the per-band split
// itself isn't needed.
func ServiceChargeTax(charge money.Money, lines []ChargeTaxLine, taxInclusive bool, taxBasisBP int) money.Money {
	var tax money.Money
	for _, b := range ApportionServiceChargeTax(charge, lines, taxInclusive, taxBasisBP) {
		tax = tax.Add(b.Tax)
	}
	return tax
}
