package pages

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// browserVoucherSlip is one gift slip printed through the browser's print
// dialog (ut-docs#6152): the paper slip's content with display-formatted
// money and no logo (parity with the browser receipt). Every string is
// already translated and formatted; the template only lays it out.
type browserVoucherSlip struct {
	StoreName string
	Header    []string
	Label     string // "Gift voucher" / "Gift voucher top-up"
	Amount    string // FormatMoneyDisplay; "+…" for a top-up
	Holder    string // "For: Anna"; "" = no row
	Balance   string // "New balance …" (top-up); "" = no row
	Code      string
	Note      string // "Reprinted <date>" (voucher page only); "" = no row
}

// browserSlipSet is what the templates execute: the slips plus the lang/dir
// of the language they are written in.
type browserSlipSet struct {
	Lang, Dir string
	Slips     []browserVoucherSlip
}

// browserVoucherSlipFor builds the voucher page's browser slip. It reuses
// voucherSlipFor so the content is the paper slip's; the amount is the
// display form (the paper's FormatMoneyPrint is bound to the printer charset).
func browserVoucherSlipFor(ctx context.Context, d *common.Deps, v data.Voucher, now time.Time) browserVoucherSlip {
	loc := httpx.DefaultLocale()
	s := voucherSlipFor(ctx, d, v, printerConfig(ctx, d), now)
	return browserVoucherSlip{
		StoreName: s.StoreName,
		Header:    s.Header,
		Label:     s.Voucher.Label,
		Amount:    httpx.FormatMoneyDisplay(v.OriginalAmountMinor, loc),
		Holder:    s.Voucher.Holder,
		Code:      s.Voucher.Code,
		Note:      s.Note,
	}
}

// receiptBrowserSlips maps the sale's issued/topped-up vouchers to browser
// slips, in the order given. Every entry is eligible: at tender time nothing
// is void (mirrors voucherSlips, Slip && Void == ""). No Reprinted note.
func receiptBrowserSlips(locale, storeName string, header []string, vs []receiptVoucherIssue) []browserVoucherSlip {
	out := make([]browserVoucherSlip, 0, len(vs))
	for _, v := range vs {
		s := browserVoucherSlip{
			StoreName: storeName,
			Header:    header,
			Label:     httpx.T(locale, "catalog.item_type.gift_voucher"),
			Amount:    httpx.FormatMoneyDisplay(v.Amount, locale),
			Code:      v.Code,
		}
		if v.TopUp {
			s.Label = httpx.T(locale, "receipt.vouchers.top_up")
			s.Amount = "+" + s.Amount
			if v.HasBalance {
				s.Balance = fmt.Sprintf(httpx.T(locale, "receipt.vouchers.new_balance"), httpx.FormatMoneyDisplay(v.Balance, locale))
			}
		}
		if v.Holder != "" {
			s.Holder = fmt.Sprintf(httpx.T(locale, "basket.gift_voucher.for"), v.Holder)
		}
		out = append(out, s)
	}
	return out
}

// receiptPrintLocale is the language the browser prints the receipt in: the
// customer's pick under the same condition renderReceipt adds its print copy
// (#4730), else the screen language.
func receiptPrintLocale(customerLocale, screenLocale string) string {
	if customerLocale != "" && customerLocale != screenLocale && httpx.InstalledLocale(customerLocale) {
		return customerLocale
	}
	return screenLocale
}

// slipDir is the text direction of a slip written in locale.
func slipDir(locale string) string {
	if httpx.IsRTL(locale) {
		return "rtl"
	}
	return "ltr"
}

// renderBrowserSlips executes one block of voucher_slip_paper.html.
func renderBrowserSlips(block, funcsLocale string, data any) (template.HTML, error) {
	t, err := httpx.ClonedTemplate("pages.voucherSlipPaper", "voucher_slip_paper.html", httpx.FuncsFor(funcsLocale),
		"ui/partials/voucher_slip_paper.html")
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, block, data); err != nil {
		return "", err
	}
	return template.HTML(buf.String()), nil //nolint:gosec // output of an html/template execution
}
