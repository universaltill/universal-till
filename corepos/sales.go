package corepos

import (
	"errors"
	"fmt"
)

type SaleInput struct {
	SaleType               string // sale|return
	SaleID                 string
	RegisterID             string
	CashierID              string
	CustomerID             string
	Currency               string
	TaxInclusive           bool
	SaleDiscount           int64 // fixed discount (minor units) applied to whole sale
	Lines                  []SaleLineInput
	Payments               []PaymentInput
	OriginalSaleID         string // for returns; creates sale_links entry when set
	Note                   string
	ReceiptNo              string
	ActorID                string
	AllowNegativeInventory bool
}

type SaleLineInput struct {
	ItemID             string
	VariantID          string
	SKU                string
	Barcode            string
	Name               string
	Qty                float64 // REAL; supports weighed items
	UnitPrice          int64   // minor units, before discount
	TaxRateBasisPoints int
	LineDiscount       int64  // fixed minor units
	LocationID         string // stock movement location
}

type PaymentInput struct {
	MethodID    string
	Amount      int64
	Currency    string
	Reference   string
	ChangeGiven int64
}

func computeSaleTotals(in SaleInput) (subtotal, taxTotal, total int64, err error) {
	for _, l := range in.Lines {
		if err := validateLine(l); err != nil {
			return 0, 0, 0, err
		}
		lineBase := AmountForQuantity(l.UnitPrice, l.Qty)
		if l.LineDiscount < 0 || l.LineDiscount > lineBase {
			return 0, 0, 0, fmt.Errorf("invalid line discount for item %s", l.ItemID)
		}
		lineNet := lineBase - l.LineDiscount
		lineTax, _ := ComputeTaxBasisPoints(lineNet, l.TaxRateBasisPoints, in.TaxInclusive)
		subtotal += lineNet
		taxTotal += lineTax
	}
	total = subtotal - in.SaleDiscount
	if !in.TaxInclusive {
		total += taxTotal
	}
	if total < 0 {
		total = 0
	}
	return subtotal, taxTotal, total, nil
}

func netPayments(payments []PaymentInput) (int64, error) {
	var sum int64
	if len(payments) == 0 {
		return 0, errors.New("sale requires at least one payment")
	}
	for i, p := range payments {
		if p.MethodID == "" {
			return 0, fmt.Errorf("payment %d missing method", i+1)
		}
		if p.Amount <= 0 {
			return 0, fmt.Errorf("payment %d amount must be > 0", i+1)
		}
		if p.ChangeGiven < 0 {
			return 0, fmt.Errorf("payment %d change must be >= 0", i+1)
		}
		if p.ChangeGiven > p.Amount {
			return 0, fmt.Errorf("payment %d change cannot exceed amount", i+1)
		}
		sum += p.Amount - p.ChangeGiven
	}
	return sum, nil
}

func validateLine(l SaleLineInput) error {
	if l.ItemID == "" && l.VariantID == "" {
		return errors.New("line requires item_id or variant_id")
	}
	if l.ItemID != "" && l.VariantID != "" {
		return errors.New("line cannot have both item_id and variant_id")
	}
	if l.Qty <= 0 {
		return errors.New("quantity must be > 0")
	}
	if l.UnitPrice < 0 {
		return errors.New("unit price must be >= 0")
	}
	if l.LocationID == "" {
		return errors.New("location_id is required")
	}
	return nil
}

// ...existing code for CompleteSale, UpdateSaleStatus, validation, audit, etc. can be moved here as needed...
