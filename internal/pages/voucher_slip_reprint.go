package pages

import (
	"context"
	"fmt"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/print"
)

// Print voucher slip from /vouchers/{id} (ut-docs#5740): one gift slip on
// its own, no receipt, no drawer kick, independent of printer.voucher_slip.

// errNoReceiptPrinter (cloudsync_print_report.go) is returned when none is set up.

// slipBalance is the balance a reprinted slip may print (ut-docs#6044):
// Known only when it is the ledger of record's, never a replica's mirror.
type slipBalance struct {
	Minor int64 // DTO boundary: data.Voucher carries minor units
	Known bool
}

// balanceLinePrinted reports whether the slip carries a balance line: a known
// balance that differs from the value printed on the slip (part used, topped
// up, corrected). Equal prints no line, as before.
func balanceLinePrinted(v data.Voucher, b slipBalance) bool {
	return b.Known && b.Minor != v.OriginalAmountMinor
}

// voucherSlipFor builds the stand-alone slip for one voucher from the
// CURRENT receipt design (store name, header lines, logo if the receipt shows
// one, printer charset) — never from the issuing sale, so an imported
// voucher prints too — in the shop's language, plus the "Reprinted <date>"
// note (Latin digits, as every printed date). The value row is always the
// original value; when bal is known and differs, a "Balance on <date>" line
// carries the current balance (ut-docs#6044).
func voucherSlipFor(ctx context.Context, d *common.Deps, v data.Voucher, bal slipBalance, cfg print.Config, now time.Time) print.VoucherSlip {
	loc := httpx.DefaultLocale()
	rd := receiptDesignFromSettings(ctx, d)
	date := httpx.FormatDateLatin(now.Local(), loc)
	vb := issuedVoucherBlock(v, loc, cfg.Charset)
	if balanceLinePrinted(v, bal) {
		vb.BalanceLabel = fmt.Sprintf(httpx.T(loc, "receipt.vouchers.balance_on_label"), date)
		vb.BalanceAmount = httpx.FormatMoneyPrint(bal.Minor, loc, cfg.Charset)
	}
	return print.VoucherSlip{
		StoreName: storeNameOrDefault(ctx, d),
		Header:    rd.Header,
		Logo:      receiptLogoRaster(rd),
		Charset:   cfg.Charset,
		Voucher:   vb,
		Note:      fmt.Sprintf(httpx.T(loc, "receipt.vouchers.reprinted"), date),
	}
}

// printVoucherSlipIn prints one voucher's slip on this till's receipt
// printer. ADR-0124 §3: refuses first, like printReceiptIn.
func printVoucherSlipIn(ctx context.Context, d *common.Deps, v data.Voucher, bal slipBalance, now time.Time) error {
	if customerDocumentsSuppressed(ctx, d) {
		return errCustomerDocumentsSuppressed
	}
	cfg := printerConfig(ctx, d)
	if !cfg.Enabled() {
		return errNoReceiptPrinter
	}
	return print.PrintVoucherSlip(ctx, cfg, voucherSlipFor(ctx, d, v, bal, cfg, now))
}
