package pages

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"html"
	"net/http"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/print"
)

// registerShiftCloseSlipPrint wires POST /api/shifts/{id}/close-slip/print
// (ut-docs#5841): prints the "Shift close" slip of a closed shift on the
// receipt printer. Never a Z. The only write is one audit_log row (entity
// shift, action shift_slip_printed), and only after the printer accepted
// the job.
func registerShiftCloseSlipPrint(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("POST /api/shifts/{id}/close-slip/print", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if err := r.ParseForm(); err != nil {
			writeXReportError(w, r, http.StatusBadRequest, "invalid_request", "shifts.close_slip.invalid_request", nil)
			return
		}
		id := strings.TrimSpace(r.PathValue("id"))
		if len(id) > xShiftIDMaxLen {
			writeXReportError(w, r, http.StatusBadRequest, "invalid_request", "shifts.close_slip.invalid_request", nil)
			return
		}
		if id == "" {
			writeXReportError(w, r, http.StatusNotFound, "shift_not_closed", "shifts.close_slip.not_closed", nil)
			return
		}
		// PostForm only: a PIN in the query string is ignored.
		pin := strings.TrimSpace(r.PostForm.Get("override_pin"))

		// Read and 404 BEFORE the PIN gate: an unknown or open shift never
		// burns a PIN or lockout slot and never renders a prompt. A closed
		// shift's figures are frozen, nothing is printed or written yet, and
		// this one read is also the fresh read for the paper.
		repo := data.NewPOSRepo(d.Db)
		slip, err := repo.ShiftCloseSlip(ctx, id, time.Now())
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) || errors.Is(err, data.ErrShiftNotClosed) {
				writeXReportError(w, r, http.StatusNotFound, "shift_not_closed", "shifts.close_slip.not_closed", nil)
				return
			}
			writeXReportError(w, r, http.StatusInternalServerError, "close_slip_failed", "shifts.close_slip.failed", err)
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
			renderElevationPrompt(w, r, r.URL.Path, "#shift-slip-print-msg",
				fmt.Sprintf(httpx.T(locale, "elevation.summary.shift_slip_print"), slip.X.RegisterName), nil, elev)
			return
		}
		actorID := elev.ActorID
		if elev.Outcome == elevated {
			actorID = elev.ApproverID
		}

		// print.PrintDoc returns nil for mode off, so a disabled printer
		// must be refused here or it would look like success.
		cfg, cerr := printerConfigChecked(ctx, d)
		if cerr != nil || !cfg.Enabled() {
			writeXReportError(w, r, http.StatusBadGateway, "print_failed", "shifts.close_slip.print_failed", cerr)
			return
		}
		doc := buildShiftSlipDoc(slip, storeNameOrDefault(ctx, d), cfg.Charset)
		pctx, cancel := context.WithTimeout(ctx, xPrintTimeout)
		defer cancel()
		if perr := print.PrintDoc(pctx, cfg, doc); perr != nil {
			writeXReportError(w, r, http.StatusBadGateway, "print_failed", "shifts.close_slip.print_failed", perr)
			return
		}

		// Audit after a successful print only; a failed audit write is
		// logged but still answers success (the paper is out). WithoutCancel:
		// the row must land even if the browser has gone away.
		actx := context.WithoutCancel(ctx)
		now := time.Now().UTC().Format(time.RFC3339)
		payload := map[string]string{"register_id": slip.X.Shift.RegisterID}
		var aerr error
		if elev.Outcome == elevated {
			aerr = repo.InsertAuditElevated(actx, nil, actorID, elev.ActorID, "shift", slip.X.Shift.ID, "shift_slip_printed", payload, now, "")
		} else {
			aerr = repo.InsertAudit(actx, nil, actorID, "shift", slip.X.Shift.ID, "shift_slip_printed", payload, now, "")
		}
		if aerr != nil {
			logging.L().Errorf("shift slip print: audit: %v", aerr)
		}

		if wantsJSON(r) {
			writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"shift_id": slip.X.Shift.ID, "printed": true}, "error": nil})
			return
		}
		// No X-UT-Response: ok header, as the X print: it would refresh
		// #shifts-page and wipe the message.
		locale := httpx.ResolveLocale(w, r)
		writeHTML(w, http.StatusOK, "<span class='x-report-print-ok' role='status'>✓ "+html.EscapeString(httpx.T(locale, "shifts.close_slip.print_ok"))+"</span>")
	})
}

// buildShiftSlipDoc lays out the "Shift close" slip for the 80mm receipt
// printer. Pure. Never a Z: no "END OF DAY"/period line, no Z or document
// number, no Barcode, Logo or TSEQR, KickDrawer false; and no X title.
func buildShiftSlipDoc(s data.ShiftCloseSlip, storeName, charset string) print.Doc {
	_, T, amt, stamp := xDocHelpers(charset)
	x := s.X
	doc := print.Doc{StoreName: storeName, Charset: charset, Meta: []string{
		T("shifts.close_slip.paper.title"),
		footerRow(T("shifts.register"), x.RegisterName),
		footerRow(T("shifts.cashier"), x.CashierName),
		footerRow(T("shifts.opened_at"), stamp(x.Shift.OpenedAt)),
		footerRow(T("shifts.closed_at"), stamp(x.Shift.ClosedAt)),
		fmt.Sprintf(T("reports.zpaper.generated"), stamp(x.GeneratedAt)),
	}}
	appendXFigures(&doc, x, charset)
	variance := amt(s.Variance)
	if s.Variance.Minor() > 0 {
		variance = "+" + variance
	}
	doc.Footer = append(doc.Footer, "", T("shifts.close_slip.paper.section_close"),
		eodRow(T("shifts.expected"), amt(s.Expected)),
		eodRow(T("shifts.counted"), amt(s.Counted)),
		eodRow(T("shifts.variance"), variance),
	)
	if s.Skim.Minor() > 0 {
		doc.Footer = append(doc.Footer, eodRow(T("shifts.skim"), "-"+amt(s.Skim)))
	}
	doc.Footer = append(doc.Footer, eodRow(T("reports.zpaper.cash.new_float"), amt(s.NewFloat)))
	return doc
}
