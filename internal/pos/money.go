package pos

import "math"

const basisPointsDen = int64(10000) // 100.00% expressed in basis points

// AmountForQuantity multiplies a unit price (minor units) by a quantity (REAL for weighed items) using half-up rounding.
func AmountForQuantity(unitPrice int64, qty float64) int64 {
	return int64(math.Round(float64(unitPrice) * qty))
}

// ComputeTaxBasisPoints calculates tax using basis points; returns (tax, total).
// Inclusive: subtotal already includes tax → compute embedded tax with half-up rounding.
// Exclusive: subtotal excludes tax → add tax on top with half-up rounding.
func ComputeTaxBasisPoints(subtotal int64, rateBasisPoints int, inclusive bool) (int64, int64) {
	if rateBasisPoints <= 0 || subtotal == 0 {
		return 0, subtotal
	}
	rate := int64(rateBasisPoints)
	if inclusive {
		den := basisPointsDen + rate
		// net = subtotal / (1 + rate); half-up rounding to avoid bias
		net := (subtotal*basisPointsDen + den/2) / den
		tax := subtotal - net
		return tax, subtotal
	}
	tax := (subtotal*rate + basisPointsDen/2) / basisPointsDen
	return tax, subtotal + tax
}
