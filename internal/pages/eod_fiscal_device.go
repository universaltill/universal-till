package pages

import (
	"context"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/fiscal"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// attachEODFiscalDevice fills rep.FiscalDevice (ut-docs#2410) — the
// Turkish YN ÖKC device's Z-status/receipt-kind evidence for [from, to),
// reconciled against the till's own "okc"-method tenders in the SAME
// window — but only for a shop this flow is FOR: Türkiye, with the
// tax-tr plugin installed and active (fiscalDeviceMarketActive, the same
// gate registerFiscalDeviceTR's mutating endpoints use). rep.FiscalDevice
// is left nil for every other shop, so a non-TR till's archived
// content_json (and printed Z-report) stay byte-for-byte unchanged.
//
// Best-effort, like every other "attach a breakdown onto an already-
// computed report" step here (see attachEODTaxBands/attachEODBands): a
// query failure is logged and swallowed, never returned, because this is
// reconciliation evidence layered onto the close, not a figure the close's
// own correctness (Gross/Net/TaxNet) depends on — the day-close must never
// fail, or worse, refuse to archive, over one plugin's own bookkeeping.
func attachEODFiscalDevice(ctx context.Context, d *common.Deps, repo *data.POSRepo, rep *data.EODReport, from, to time.Time) {
	if !fiscalDeviceMarketActive(ctx, d) {
		return
	}
	win, err := repo.FiscalDeviceWindow(ctx, fiscal.MethodKeyOKC, from, to)
	if err != nil {
		logging.L().Errorf("eod fiscal device window: %v", err)
		return
	}
	rep.FiscalDevice = &win
}

// localDayWindow turns two calendar days (as time.Parse("2006-01-02")
// yields them: UTC midnight) into the half-open instant window
// [from 00:00 local, to+1 day 00:00 local) that matches EndOfDayRange's
// local-calendar-day bucketing (ut-docs#869) — so the device receipts and
// the till's own tenders are counted over exactly the days the rest of the
// range report covers, not a UTC window shifted by the shop's offset
// (three hours, in Türkiye).
func localDayWindow(fromDate, toDate time.Time) (time.Time, time.Time) {
	from := time.Date(fromDate.Year(), fromDate.Month(), fromDate.Day(), 0, 0, 0, 0, time.Local)
	to := time.Date(toDate.Year(), toDate.Month(), toDate.Day(), 0, 0, 0, 0, time.Local).AddDate(0, 0, 1)
	return from, to
}
