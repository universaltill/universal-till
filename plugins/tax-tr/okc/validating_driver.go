package okc

// validatingDriver wraps a Driver so a nil-error Sale/Refund answer with no
// usable receipt number becomes ErrNoReceipt — the one seam every Driver's
// answer passes through (ut-docs#1780). Before this, the check lived only
// inside BridgeDriver.Sale/Refund, so a future maker driver could ship
// without re-deriving it; NewDriver now wraps every driver it constructs,
// so the invariant holds regardless of whether a given Driver checks it
// itself.
type validatingDriver struct {
	Driver
}

// NewValidatingDriver wraps d so the "no usable receipt_no → refuse"
// invariant (ut-docs#1763) is enforced for its Sale and Refund regardless
// of whether d already enforces it internally. Status is unaffected.
func NewValidatingDriver(d Driver) Driver {
	return validatingDriver{Driver: d}
}

func (d validatingDriver) Sale(req SaleRequest) (Evidence, error) {
	return requireReceipt(d.Driver.Sale(req))
}

func (d validatingDriver) Refund(req RefundRequest) (Evidence, error) {
	return requireReceipt(d.Driver.Refund(req))
}

// requireReceipt turns a nil-error answer with no usable receipt number
// into ErrNoReceipt; any other error passes through unchanged.
func requireReceipt(ev Evidence, err error) (Evidence, error) {
	if err != nil {
		return ev, err
	}
	if isBlankReceiptNo(ev.ReceiptNo) {
		return Evidence{}, ErrNoReceipt
	}
	return ev, nil
}
