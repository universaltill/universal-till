package pos

type TaxEngine interface {
	Compute(subtotalCents int64) (taxCents int64, totalCents int64)
}

type PercentTaxEngine struct {
	RatePercent int  // e.g. 20 for 20%
	Inclusive   bool // if true, subtotal already includes tax
}

func (e PercentTaxEngine) Compute(subtotal int64) (int64, int64) {
	if e.RatePercent <= 0 {
		return 0, subtotal
	}
	if e.Inclusive {
		// tax = subtotal - subtotal/(1+rate)
		// do integer math: tax = subtotal - floor(subtotal*100/(100+rate)) with rate as percent
		den := int64(100 + e.RatePercent)
		net := (subtotal * 100) / den
		tax := subtotal - net
		return tax, subtotal
	}
	// exclusive
	tax := subtotal * int64(e.RatePercent) / 100
	return tax, subtotal + tax
}
