package pages

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// Selling a gift voucher through a gift_voucher item (ut-docs#4214 slice A).
// The item never adds goods: its tap/scan opens the Gift voucher popup
// (gift_voucher_popup.html), whose submit adds a voucher basket line
// (pos.Service.AddGiftVoucherLine). Checkout turns that line into the
// existing #1008/#1037 voucher issue (giftVoucherIssuesFromLines below).

// giftVoucherPopup is gift_voucher_popup.html's view model.
type giftVoucherPopup struct {
	Code, Src, ItemName string
	// PrefillMinor is the value field's start ("" = empty): the item's own
	// price when above zero on a first ask, the entered value on a re-ask.
	PrefillMinor string
	Number       string
	Holder       string
	Purpose      string
	ErrMsg       string
	MaxMinor     int64
}

// renderGiftVoucherPopup writes the popup into #modifier-modal, the same
// retarget the ut-docs#2227 variant picker uses.
func renderGiftVoucherPopup(w http.ResponseWriter, r *http.Request, vm giftVoucherPopup) {
	vm.MaxMinor = pos.MaxVoucherIssueAmount.Minor()
	w.Header().Set("HX-Retarget", "#modifier-modal")
	w.Header().Set("HX-Reswap", "innerHTML")
	w.Header().Set("HX-Trigger-After-Swap", "open-modifier-modal")
	httpx.RenderPartial("ui/partials/gift_voucher_popup.html", vm)(w, r)
}

// firstGiftVoucherPopup is the popup's first ask for base: value prefilled
// from the item's price when above zero.
func firstGiftVoucherPopup(code, src string, base pos.BasketLine) giftVoucherPopup {
	vm := giftVoucherPopup{Code: code, Src: src, ItemName: base.Name, Purpose: data.VoucherPurposeMulti}
	if base.PriceCents.IsPositive() {
		vm.PrefillMinor = strconv.FormatInt(base.PriceCents.Minor(), 10)
	}
	return vm
}

// giftVoucherNumberMaxLen mirrors the issue path's own bound
// (pos_api.go's IssueVouchers check, pos.validateVoucherID).
const giftVoucherNumberMaxLen = 64

// handleGiftVoucherScan answers /api/pos/scan for a gift_voucher item. With
// no gv=1 in the request it opens the popup; with it, it validates the
// popup's fields and adds the voucher line, or re-renders the popup with an
// inline error. render swaps the basket (the scan handler's own renderer).
func handleGiftVoucherScan(w http.ResponseWriter, r *http.Request, d *common.Deps, repo *data.POSRepo, locale, code, src string, base pos.BasketLine, render func(*pos.Basket)) {
	if r.Form.Get("gv") != "1" {
		renderGiftVoucherPopup(w, r, firstGiftVoucherPopup(code, src, base))
		return
	}
	vm := giftVoucherPopup{
		Code: code, Src: src, ItemName: base.Name,
		PrefillMinor: strings.TrimSpace(r.Form.Get("gv_value_minor")),
		Number:       strings.TrimSpace(r.Form.Get("gv_code")),
		Holder:       strings.TrimSpace(r.Form.Get("gv_holder")),
		Purpose:      data.VoucherPurposeMulti,
	}
	if r.Form.Get("gv_purpose") == data.VoucherPurposeSingle {
		vm.Purpose = data.VoucherPurposeSingle
	}
	vm.Holder = boundGiftVoucherHolder(vm.Holder)
	fail := func(msg string) {
		vm.ErrMsg = msg
		renderGiftVoucherPopup(w, r, vm)
	}
	value, err := strconv.ParseInt(vm.PrefillMinor, 10, 64)
	if err != nil || value <= 0 || money.FromMinor(value) > pos.MaxVoucherIssueAmount {
		vm.PrefillMinor = ""
		fail(fmt.Sprintf(httpx.T(locale, "pos.gift_voucher.error.value"), httpx.FormatMoneyDisplay(pos.MaxVoucherIssueAmount.Minor(), locale)))
		return
	}
	if len(vm.Number) > giftVoucherNumberMaxLen || strings.IndexFunc(vm.Number, func(c rune) bool { return c < 0x20 || c == 0x7f }) >= 0 {
		fail(httpx.T(locale, "pos.gift_voucher.error.number_invalid"))
		return
	}
	if countGiftVoucherLines(d.Engine.Lines()) >= pos.MaxVoucherIssuesPerSale {
		fail(fmt.Sprintf(httpx.T(locale, "pos.gift_voucher.error.too_many"), pos.MaxVoucherIssuesPerSale))
		return
	}
	number := vm.Number
	if number != "" {
		if d.Engine.HasGiftVoucherCode(number) {
			fail(httpx.T(locale, "pos.gift_voucher.error.number_in_sale"))
			return
		}
		inUse, err := giftVoucherNumberInUse(r.Context(), d, repo, number, true)
		if err != nil {
			logging.L().Errorf("gift voucher: check number %q: %v", number, err)
			fail(httpx.T(locale, "pos.gift_voucher.error.check_failed"))
			return
		}
		if inUse {
			fail(httpx.T(locale, "pos.gift_voucher.error.number_in_use"))
			return
		}
	} else {
		number, err = generateGiftVoucherNumber(r.Context(), d, repo)
		if err != nil {
			logging.L().Errorf("gift voucher: generate number: %v", err)
			fail(httpx.T(locale, "pos.gift_voucher.error.check_failed"))
			return
		}
	}
	b, err := d.Engine.AddGiftVoucherLine(base, money.FromMinor(value), pos.GiftVoucherLine{Code: number, HolderLabel: vm.Holder, Purpose: vm.Purpose})
	if errors.Is(err, pos.ErrGiftVoucherCodeInBasket) {
		fail(httpx.T(locale, "pos.gift_voucher.error.number_in_sale"))
		return
	}
	if err != nil {
		logging.L().Errorf("gift voucher: add line: %v", err)
		fail(httpx.T(locale, "pos.gift_voucher.error.check_failed"))
		return
	}
	// Tells the popup's `ut-ok` guard the line landed, so it closes; a
	// re-rendered popup above carries no such header and stays open.
	w.Header().Set("X-UT-Response", "ok")
	render(b)
}

// giftVoucherNumberInUse reports whether a voucher with this number already
// exists: this till's own vouchers table first, then — on a till that
// follows a main till, and only for a number the cashier typed or scanned
// (askPrimary) — the main till, the same two-step lookup a voucher scan
// uses. An unreachable main till is not "in use": the sale stays offline-
// capable, and CompleteSale's own unique id refuses a real clash.
func giftVoucherNumberInUse(ctx context.Context, d *common.Deps, repo *data.POSRepo, number string, askPrimary bool) (bool, error) {
	_, err := repo.GetVoucherBalance(ctx, nil, number)
	if err == nil {
		return true, nil
	}
	if !errors.Is(err, data.ErrVoucherNotFound) {
		return false, err
	}
	if askPrimary {
		if _, ok := fetchVoucherFromPrimary(ctx, d, voucherProxyClient, number); ok {
			return true, nil
		}
	}
	return false, nil
}

// giftVoucherNumberPrefix is the GS1 "coupon" prefix (99): a generated
// number is a valid EAN-13 every scanner reads, that can't be mistaken for
// a GS1 product code or an in-store price/weight label (prefix 2).
const giftVoucherNumberPrefix = "99"

// generateGiftVoucherNumber returns a fresh 13-digit EAN-13 voucher number
// ("99" + 10 random digits + check digit), unique in this till's vouchers
// table and in the current basket, retrying on a clash.
func generateGiftVoucherNumber(ctx context.Context, d *common.Deps, repo *data.POSRepo) (string, error) {
	limit := big.NewInt(10_000_000_000)
	for attempt := 0; attempt < 20; attempt++ {
		n, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", err
		}
		body := fmt.Sprintf("%s%010d", giftVoucherNumberPrefix, n.Int64())
		code := body + strconv.Itoa(ean13CheckDigit(body))
		if d.Engine.HasGiftVoucherCode(code) {
			continue
		}
		inUse, err := giftVoucherNumberInUse(ctx, d, repo, code, false)
		if err != nil {
			return "", err
		}
		if !inUse {
			return code, nil
		}
	}
	return "", errors.New("no free gift voucher number after 20 attempts")
}

// ean13CheckDigit is the GS1 mod-10 check digit for a 12-digit body.
func ean13CheckDigit(body string) int {
	sum := 0
	for i, c := range body {
		n := int(c - '0')
		if i%2 == 1 {
			n *= 3
		}
		sum += n
	}
	return (10 - sum%10) % 10
}

func countGiftVoucherLines(lines []pos.BasketLine) int {
	n := 0
	for _, l := range lines {
		if l.GiftVoucher != nil {
			n++
		}
	}
	return n
}

// giftVoucherIssuesFromLines splits a basket at checkout (ut-docs#4214): the
// goods lines, one tenderVoucherIssue per gift voucher line (face value,
// number, holder, use; a specific-item voucher at the line's effective tax
// rate), and any specific-item voucher line whose rate the tax plugin can't
// answer right now (it blocks the tender like a goods line would, #368).
func giftVoucherIssuesFromLines(lines []pos.BasketLine, eng *pos.Service) (goods []pos.BasketLine, issues []tenderVoucherIssue, taxBlocked []pos.BasketLine) {
	for _, l := range lines {
		if l.GiftVoucher == nil {
			goods = append(goods, l)
			continue
		}
		iss := tenderVoucherIssue{
			Amount:      pos.AmountForQuantity(l.PriceCents, l.Qty).Minor(),
			Code:        l.GiftVoucher.Code,
			HolderLabel: l.GiftVoucher.HolderLabel,
			Purpose:     data.VoucherPurposeMulti,
			// ut-docs#4252: a top-up line credits the existing voucher.
			// Always multi-purpose (AddGiftVoucherLine refuses otherwise),
			// so it never reaches the single-purpose branch below.
			TopUp: l.GiftVoucher.TopUp,
		}
		if l.GiftVoucher.IsSinglePurpose() && !l.GiftVoucher.TopUp {
			rate, blocked := eng.EffectiveLineTaxRateBP(l)
			if blocked {
				taxBlocked = append(taxBlocked, l)
				continue
			}
			iss.Purpose = data.VoucherPurposeSingle
			iss.VATRateBP = rate
		}
		issues = append(issues, iss)
	}
	return goods, issues, taxBlocked
}

// boundGiftVoucherHolder cuts the optional "for" name to at most 200 BYTES,
// on a rune boundary: the tender's issue validation bounds HolderLabel by
// bytes, so a longer name (e.g. Arabic, 2 bytes a letter) would pass the
// popup and then fail checkout (ut-docs#4214 review).
func boundGiftVoucherHolder(s string) string {
	if len(s) <= 200 {
		return s
	}
	cut := 200
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
