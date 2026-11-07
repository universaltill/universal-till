package pages

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/print"
)

const (
	xShiftIDMaxLen = 128              // same cap as xRegisterMaxLen
	xPrintTimeout  = 15 * time.Second // as the EOD print / cloudPrintReport
)

// registerShiftXReportPrint wires POST /api/shifts/{id}/x-report/print
// (ut-docs#5798): prints the open shift's X report on the receipt printer.
// The only write is one audit_log row (entity shift, action
// x_report_printed), and only after the printer accepted the job.
func registerShiftXReportPrint(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("POST /api/shifts/{id}/x-report/print", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if err := r.ParseForm(); err != nil {
			writeXReportError(w, r, http.StatusBadRequest, "invalid_request", "shifts.x_report.invalid_request", nil)
			return
		}
		id := strings.TrimSpace(r.PathValue("id"))
		if len(id) > xShiftIDMaxLen {
			writeXReportError(w, r, http.StatusBadRequest, "invalid_request", "shifts.x_report.invalid_request", nil)
			return
		}
		if id == "" {
			writeXReportError(w, r, http.StatusNotFound, "no_open_shift", "shifts.x_report.no_open_shift", nil)
			return
		}
		// PostForm only: a PIN in the query string is ignored (unlike the
		// EOD reprint's FormValue).
		pin := strings.TrimSpace(r.PostForm.Get("override_pin"))

		// 404 before PIN: an unknown or closed shift never burns a PIN or
		// lockout slot and never renders a prompt.
		repo := data.NewPOSRepo(d.Db)
		open, err := repo.ListOpenShifts(ctx)
		if err != nil {
			writeXReportError(w, r, http.StatusInternalServerError, "x_report_failed", "shifts.x_report.failed", err)
			return
		}
		var sel data.OpenShift
		found := false
		for _, o := range open {
			if o.ShiftID == id {
				sel, found = o, true
				break
			}
		}
		if !found {
			writeXReportError(w, r, http.StatusNotFound, "no_open_shift", "shifts.x_report.no_open_shift", nil)
			return
		}

		elev := checkOrElevate(d, r, "reports", pin)
		if elev.Outcome == needsElevation {
			if wantsJSON(r) {
				status := http.StatusForbidden
				if errors.Is(elev.Err, auth.ErrLockedOut) {
					status = http.StatusTooManyRequests
				}
				writeXReportError(w, r, status, "manager_pin_required", "common.error.manager_or_admin_required", nil)
				return
			}
			locale := httpx.ResolveLocale(w, r)
			renderElevationPrompt(w, r, r.URL.Path, "#x-report-print-msg",
				fmt.Sprintf(httpx.T(locale, "elevation.summary.x_report_print"), sel.RegisterName), nil, elev)
			return
		}
		actorID := elev.ActorID
		if elev.Outcome == elevated {
			actorID = elev.ApproverID
		}

		// Fresh read: the paper shows "as of now", not the on-screen figures.
		rep, err := repo.ShiftXReport(ctx, id, time.Now())
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeXReportError(w, r, http.StatusNotFound, "no_open_shift", "shifts.x_report.no_open_shift", nil)
				return
			}
			writeXReportError(w, r, http.StatusInternalServerError, "x_report_failed", "shifts.x_report.failed", err)
			return
		}
		if !rep.Shift.Open { // closed between the list and the read
			writeXReportError(w, r, http.StatusNotFound, "no_open_shift", "shifts.x_report.no_open_shift", nil)
			return
		}

		// print.PrintDoc returns nil for mode off, so a disabled printer
		// must be refused here or it would look like success.
		cfg, cerr := printerConfigChecked(ctx, d)
		if cerr != nil || !cfg.Enabled() {
			writeXReportError(w, r, http.StatusBadGateway, "print_failed", "settings.printer.test_failed", cerr)
			return
		}
		doc := buildXReportDoc(rep, storeNameOrDefault(ctx, d), cfg.Charset)
		pctx, cancel := context.WithTimeout(ctx, xPrintTimeout)
		defer cancel()
		if perr := print.PrintDoc(pctx, cfg, doc); perr != nil {
			writeXReportError(w, r, http.StatusBadGateway, "print_failed", "settings.printer.test_failed", perr)
			return
		}

		// Audit after a successful print only. A failed audit write is
		// logged but still answers success: the paper is out, and a 5xx
		// would invite a second print.
		now := time.Now().UTC().Format(time.RFC3339)
		payload := map[string]string{"register_id": rep.Shift.RegisterID}
		var aerr error
		if elev.Outcome == elevated {
			aerr = repo.InsertAuditElevated(ctx, nil, actorID, elev.ActorID, "shift", rep.Shift.ID, "x_report_printed", payload, now, "")
		} else {
			aerr = repo.InsertAudit(ctx, nil, actorID, "shift", rep.Shift.ID, "x_report_printed", payload, now, "")
		}
		if aerr != nil {
			logging.L().Errorf("x report print: audit: %v", aerr)
		}

		if wantsJSON(r) {
			writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"shift_id": rep.Shift.ID, "printed": true}, "error": nil})
			return
		}
		// No X-UT-Response: ok header: it would make the dialog's
		// elevation-done refresh #shifts-page and wipe the printed figures.
		locale := httpx.ResolveLocale(w, r)
		writeHTML(w, http.StatusOK, "<span class='x-report-print-ok' role='status'>✓ "+html.EscapeString(httpx.T(locale, "shifts.x_report.print_ok"))+"</span>")
	})
}

// buildXReportDoc lays out the X report for the 80mm receipt printer. Pure.
// Same print-locale/money/date convention as buildEODDoc (store locale,
// Latin digits, ut-docs#1130). Never a Z: no "END OF DAY"/period line, no
// Z number, no series, no Barcode, no Logo, no TSEQR, KickDrawer false.
func buildXReportDoc(rep data.XReport, storeName, charset string) print.Doc {
	loc := httpx.DefaultLocale()
	T := func(k string) string { return httpx.T(loc, k) }
	amt := func(m money.Money) string { // sign-first, as buildEODDoc's signed
		if v := m.Minor(); v < 0 {
			return "-" + httpx.FormatMoneyPrint(-v, loc, charset)
		}
		return httpx.FormatMoneyPrint(m.Minor(), loc, charset)
	}
	stamp := func(s string) string { // stored time -> local Latin "date HH:MM"
		if t, ok := httpx.ParseStoredTime(s); ok {
			return httpx.FormatDateTimeLatin(t.Local(), loc)
		}
		return s
	}
	doc := print.Doc{StoreName: storeName, Charset: charset, Meta: []string{
		T("shifts.x_report.paper.title"),
		T("shifts.x_report.paper.not_a_close"),
		footerRow(T("shifts.register"), rep.RegisterName),
		footerRow(T("shifts.cashier"), rep.CashierName),
		footerRow(T("shifts.opened_at"), stamp(rep.Shift.OpenedAt)),
		fmt.Sprintf(T("reports.zpaper.generated"), stamp(rep.GeneratedAt)),
	}}
	doc.Totals = []print.KV{
		{Label: fmt.Sprintf(T("reports.zpaper.sales"), rep.SalesCount), Amount: amt(rep.SalesGross)},
		{Label: fmt.Sprintf(T("reports.zpaper.refunds"), rep.ReturnsCount), Amount: "-" + amt(rep.ReturnsTotal)},
		{Label: T("reports.zpaper.tax_net"), Amount: amt(rep.Tax)},
		{Label: T("reports.zpaper.net"), Amount: amt(rep.Net), Strong: true},
	}
	for _, m := range rep.Methods {
		doc.Payments = append(doc.Payments, print.KV{Label: xMethodLabel(loc, m), Amount: amt(m.Net)})
	}
	doc.Footer = append(doc.Footer, "",
		eodRow(fmt.Sprintf(T("reports.zpaper.voided"), rep.VoidsCount), amt(rep.VoidsTotal)),
		eodRow(fmt.Sprintf(T("reports.zpaper.cleared_baskets"), rep.ClearedBasketsCount), amt(rep.ClearedBasketsTotal)),
		"", T("shifts.x_report.section_cash"),
		eodRow(T("reports.zpaper.cash.opening_float"), amt(rep.Cash.Opening)),
		eodRow(T("reports.zpaper.cash.cash_sales"), amt(rep.Cash.CashSales)),
		eodRow(T("reports.zpaper.cash.tips_held_out"), amt(rep.Cash.TipsHeldOut)), // always: the block must add up on paper
	)
	for _, a := range rep.Cash.Adjustments {
		label := T("shifts.adjust")
		switch a.Type {
		case "payout":
			label = T("shifts.payout")
		case "skim":
			label = T("shifts.skim")
		}
		if a.Reason != "" {
			label += ": " + a.Reason
		}
		doc.Footer = append(doc.Footer, footerRow(label, amt(a.Amount))) // clips the label, never the amount
	}
	doc.Footer = append(doc.Footer,
		eodRow(T("shifts.x_report.adjustments_total"), amt(rep.Cash.AdjustmentsTotal)),
		eodRow(T("shifts.expected"), amt(rep.Cash.Expected)),
		"", footerRow(T("reports.zpaper.no_sale_total"), strconv.Itoa(rep.NoSale)),
	)
	if rep.FirstReceipt != "" {
		doc.Footer = append(doc.Footer, "", fmt.Sprintf(T("reports.zpaper.receipts_range"), rep.FirstReceipt, rep.LastReceipt))
	}
	return doc
}

// xMethodLabel: the core tender key when the id is a core one, else the
// method's own name through T (translates a "tender.*" name, returns a
// literal custom name unchanged).
func xMethodLabel(locale string, m data.XMethodLine) string {
	if key, ok := httpx.PaymentMethodKey(m.MethodID); ok {
		return httpx.T(locale, key)
	}
	return httpx.T(locale, m.Name)
}
