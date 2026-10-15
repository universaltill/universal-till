package pos

import (
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/money"
)

// GiftVoucherLine (ut-docs#4214) marks a basket line as a gift voucher being
// SOLD: the line's PriceCents is the voucher's face value, and checkout turns
// the line into a VoucherIssueInput (the existing #1008/#1037 issue path)
// instead of a sale line — no product revenue, no stock movement, no basket
// discount. Code is the voucher number (a pre-printed card's number, or one
// the till generated when the line was added), HolderLabel the optional
// "for" name, Purpose data.VoucherPurposeMulti (any purchase — the default)
// or data.VoucherPurposeSingle (a specific item, taxed now at the item's own
// rate).
type GiftVoucherLine struct {
	Code        string `json:"code"`
	HolderLabel string `json:"holder_label,omitempty"`
	Purpose     string `json:"purpose,omitempty"`
	// TopUp (ut-docs#4252) marks the line as adding value to the EXISTING
	// voucher Code rather than selling a new one: checkout turns it into a
	// VoucherIssueInput with TopUp, which credits that voucher. Always
	// multi-purpose (a single-purpose voucher's VAT was fixed at issue).
	TopUp bool `json:"top_up,omitempty"`
}

// IsSinglePurpose reports whether the voucher is a #1037 single-purpose
// ("specific item") voucher, taxed at issue.
func (v *GiftVoucherLine) IsSinglePurpose() bool {
	return v != nil && v.Purpose == data.VoucherPurposeSingle
}

// ErrGiftVoucherCodeInBasket refuses a second voucher line with a number
// already on another voucher line of this basket.
var ErrGiftVoucherCodeInBasket = errors.New("gift voucher number already in this sale")

// ErrNotGiftVoucherItem refuses AddGiftVoucherLine for a standard item.
var ErrNotGiftVoucherItem = errors.New("not a gift voucher item")

// IsGiftVoucherItem reports whether a resolved line is a gift_voucher item
// (catalogtypes.ItemTypeGiftVoucher). Such an item is never added as goods:
// scanQty and AddLineWithModifiers refuse it, and the scan handler routes it
// to the voucher popup, which adds it through AddGiftVoucherLine.
func (l BasketLine) IsGiftVoucherItem() bool {
	return l.ItemType == catalogtypes.ItemTypeGiftVoucher
}

// AddGiftVoucherLine adds a gift voucher line (ut-docs#4214): qty 1 at the
// entered face value, NoMerge (every voucher is its own line), never cached,
// never discounted. v.Code must be non-empty and not already on another
// voucher line of this basket (ErrGiftVoucherCodeInBasket) — the caller
// checks the vouchers table and generates a number for a blank one. An
// empty Purpose is stored as data.VoucherPurposeMulti.
func (s *Service) AddGiftVoucherLine(base BasketLine, value money.Money, v GiftVoucherLine) (*Basket, error) {
	if !base.IsGiftVoucherItem() {
		return nil, ErrNotGiftVoucherItem
	}
	v.Code = strings.TrimSpace(v.Code)
	v.HolderLabel = strings.TrimSpace(v.HolderLabel)
	if v.Purpose == "" {
		v.Purpose = data.VoucherPurposeMulti
	}
	if v.TopUp && v.Purpose != data.VoucherPurposeMulti {
		return nil, errors.New("a gift voucher top-up is always for any purchase")
	}
	if v.Code == "" || !value.IsPositive() {
		return nil, errors.New("gift voucher needs a number and a value above zero")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hasGiftVoucherCodeLocked(v.Code) {
		return nil, ErrGiftVoucherCodeInBasket
	}
	line := base
	line.PriceCents = value
	line.Qty = 1
	line.QtyFromCode = false
	line.NoMerge = true
	line.LineDiscount = 0
	line.LineKey = uuid.NewString()
	line.AgeRestricted = false
	line.OrderType = s.orderType
	line.setModifiers(nil)
	line.GiftVoucher = &v
	s.mergeResolved(line)
	return s.basketCopyLocked(), nil
}

// HasGiftVoucherCode reports whether a voucher line of the current basket
// already carries code.
func (s *Service) HasGiftVoucherCode(code string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hasGiftVoucherCodeLocked(strings.TrimSpace(code))
}

func (s *Service) hasGiftVoucherCodeLocked(code string) bool {
	if code == "" {
		return false
	}
	for _, l := range s.lines {
		if l.GiftVoucher != nil && strings.EqualFold(l.GiftVoucher.Code, code) {
			return true
		}
	}
	return false
}
