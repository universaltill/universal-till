package pos

import "github.com/universaltill/universal-till/internal/money"

type TaxEngine interface {
	Compute(subtotal money.Money) (tax money.Money, total money.Money)
}

type PercentTaxEngine struct {
	RatePercent int  // e.g. 20 for 20%
	Inclusive   bool // if true, subtotal already includes tax
}

// BasisPointsTaxEngine uses basis points (1/100th of a percent) to align with DB storage.
type BasisPointsTaxEngine struct {
	RateBasisPoints int
	Inclusive       bool
}

func (e PercentTaxEngine) Compute(subtotal money.Money) (money.Money, money.Money) {
	if e.RatePercent <= 0 {
		return 0, subtotal
	}
	if e.Inclusive {
		// tax = subtotal - subtotal/(1+rate)
		// do integer math: tax = subtotal - floor(subtotal*100/(100+rate)) with rate as percent
		den := int64(100 + e.RatePercent)
		net := money.FromMinor((subtotal.Minor() * 100) / den)
		tax := subtotal.Sub(net)
		return tax, subtotal
	}
	// exclusive
	tax := money.FromMinor(subtotal.Minor() * int64(e.RatePercent) / 100)
	return tax, subtotal.Add(tax)
}

func (e BasisPointsTaxEngine) Compute(subtotal money.Money) (money.Money, money.Money) {
	return ComputeTaxBasisPoints(subtotal, e.RateBasisPoints, e.Inclusive)
}
