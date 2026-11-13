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

// voucherSlipFor builds the stand-alone slip for one voucher from the
// CURRENT receipt design (store name, header lines, logo if the receipt shows
// one, printer charset) — never from the issuing sale, so an imported
// voucher prints too — in the shop's language, plus the "Reprinted <date>"
// note (Latin digits, as every printed date). ut-docs#6179: it also carries
// the "Issued <date>" line (the reprint date never replaces it; none for a
// voucher with no issuing sale — an imported opening balance's created_at is
// the import time — nor for an unparseable timestamp) and the shop's slip text.
func voucherSlipFor(ctx context.Context, d *common.Deps, v data.Voucher, cfg print.Config, now time.Time) print.VoucherSlip {
	loc := httpx.DefaultLocale()
	rd := receiptDesignFromSettings(ctx, d)
	issuedDate := ""
	if v.IssuedSaleID != "" {
		issuedDate = slipDateLine(loc, "receipt.vouchers.issued_on", v.CreatedAt)
	}
	return print.VoucherSlip{
		StoreName: storeNameOrDefault(ctx, d),
		Header:    rd.Header,
		Logo:      receiptLogoRaster(rd),
		Charset:   cfg.Charset,
		Voucher:   issuedVoucherBlock(v, loc, cfg.Charset),
		Date:      issuedDate,
		Text:      voucherSlipLines(rd.VoucherSlipText),
		Note:      fmt.Sprintf(httpx.T(loc, "receipt.vouchers.reprinted"), httpx.FormatDateLatin(now.Local(), loc)),
	}
}

// printVoucherSlipIn prints one voucher's slip on this till's receipt
// printer. ADR-0124 §3: refuses first, like printReceiptIn.
func printVoucherSlipIn(ctx context.Context, d *common.Deps, v data.Voucher, now time.Time) error {
	if customerDocumentsSuppressed(ctx, d) {
		return errCustomerDocumentsSuppressed
	}
	cfg := printerConfig(ctx, d)
	if !cfg.Enabled() {
		return errNoReceiptPrinter
	}
	return print.PrintVoucherSlip(ctx, cfg, voucherSlipFor(ctx, d, v, cfg, now))
}
