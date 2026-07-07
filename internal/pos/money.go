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
