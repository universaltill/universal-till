package pos

import "github.com/universaltill/universal-till/internal/money"

const basisPointsDen = int64(10000) // 100.00% expressed in basis points

// AmountForQuantity multiplies a unit price by a quantity (REAL for weighed
// items) using half-away-from-zero rounding.
func AmountForQuantity(unitPrice money.Money, qty float64) money.Money {
	return unitPrice.MulQty(qty)
}

// ComputeTaxBasisPoints calculates tax using basis points; returns (tax, total).
// Inclusive: subtotal already includes tax → compute embedded tax with half-up rounding.
// Exclusive: subtotal excludes tax → add tax on top with half-up rounding.
//
// Gross-inclusive invariant (ut-docs#1014): for an inclusive (gross-priced)
// catalog, `total` here is always the caller's `subtotal` returned
// unchanged — changing `rateBasisPoints` (e.g. a dine-in/takeaway order-type
// switch, §12 UStG) only moves `tax`/the derived net between `subtotal` and
// `tax`, and NEVER the gross figure itself. A German café's cappuccino sold
// at 19% dine-in or 7% takeaway must show the same 4.80 either way — only
// the embedded net/tax split changes.
//
// The actual guarantor is `tax := subtotal.Sub(net)` below, NOT the fact
// that `total` is returned as `subtotal` verbatim on its own — since
// `tax` is defined as `subtotal - net`, `net + tax` is algebraically
// identical to `subtotal` regardless of how `net` was computed, so
// returning `net.Add(tax)` instead would be an equivalent no-op, not a
// bug. What WOULD reintroduce the historical bug is computing `tax`
// independently (e.g. `net.MulDiv(rate, basisPointsDen)`) rather than as
// `subtotal`'s complement of `net` — that decouples `tax` from `subtotal`
// and a rounding difference on `net` then leaks into `total`. Keep `tax`
// derived as `subtotal.Sub(net)`, never computed independently, and this
// invariant holds regardless of exactly how `total` is expressed. The
// exclusive branch is deliberately the mirror image: there, `subtotal`
// (net) is what stays fixed and the returned total (gross) is free to
// move with the rate — the two pricing modes must stay behaviourally
// distinct, never collapsed into one.
func ComputeTaxBasisPoints(subtotal money.Money, rateBasisPoints int, inclusive bool) (money.Money, money.Money) {
	if rateBasisPoints <= 0 || subtotal.IsZero() {
		return 0, subtotal
	}
	rate := int64(rateBasisPoints)
	if inclusive {
		den := basisPointsDen + rate
		// net = subtotal / (1 + rate); half-up rounding to avoid bias.
		// MulDiv(basisPointsDen, den) == (subtotal*10000 + den/2)/den (unchanged).
		net := subtotal.MulDiv(basisPointsDen, den)
		tax := subtotal.Sub(net)
		return tax, subtotal
	}
	// MulDiv(rate, basisPointsDen) == (subtotal*rate + 10000/2)/10000 (unchanged).
	tax := subtotal.MulDiv(rate, basisPointsDen)
	return tax, subtotal.Add(tax)
}
