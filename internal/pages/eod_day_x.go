package pages

import (
	"context"
	"errors"
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

// ut-docs#5800: the till-wide "day so far" X report on Reports -> End of day.
// It shows and prints what the NEXT Z would contain if it ran now: the same
// [from, now) window and the same Z layout, titled as an X. Read-only: no
// archive row, no settings write, no eod_generated audit, no fiscal action,
// no drawer kick. The only write is one audit row after a successful print.

// readEODDayX reads the figures of the next Z as of now.
//
// It deliberately does NOT take HoldWriteLock. The lock exists so the
// ARCHIVED boundary of a Z is exact for the next one (ut-docs#5769); a preview
// archives nothing, so there is no boundary to protect, and holding it would
// stall every checkout (up to 4 s) on each tap. The accepted cost: a row that
// commits while EndOfDayInstant's many independent queries run (totals, voids,
// methods, departments, tills, tips, articles, operators, bands) can show in
// one section and not another, so ANY section of this one preview can differ
// by that sale. The next Z is unaffected. Reading it all in one snapshot is a
// follow-up. It also skips attachEODFiscalDevice: that prints device Z
// numbers, and an X takes no fiscal action. Annotation stays unset.
func readEODDayX(ctx context.Context, d *common.Deps, actorName string) (data.EODReport, error) {
	repo := data.NewPOSRepo(d.Db)
	from, err := eodWindowFrom(ctx, d, repo)
	if err != nil {
		return data.EODReport{}, err
	}
	rep, err := readEODWindow(ctx, repo, from, time.Now())
	if err != nil {
		return rep, err
	}
	rep.GeneratedBy = actorName
	return rep, nil
}

// buildEODDayXDoc is the Z paper (buildEODDoc) with two extra leading Meta
// lines, the X title and "not a day close". Reusing the Z builder keeps the
// layout the Z's by construction. rep.Day stays empty (never "END OF DAY") and
// the fiscal-device block and annotation are cleared: no Z identity.
func buildEODDayXDoc(rep data.EODReport, storeName, charset, articleMode string, articleCap int) print.Doc {
	rep.FiscalDevice = nil
	rep.Annotation = ""
	doc := buildEODDoc(rep, storeName, charset, articleMode, articleCap)
	loc := httpx.DefaultLocale()
	doc.Meta = append([]string{
		httpx.T(loc, "reports.eod.day_x.paper.title"),
		httpx.T(loc, "shifts.x_report.paper.not_a_close"),
	}, doc.Meta...)
	return doc
}

// dayXActor is who the report is attributed to: the approver after a PIN
// elevation (screen and paper agree), else the signed-in user.
func dayXActor(elev elevationCheck) string {
	if elev.Outcome == elevated {
		return elev.ApproverID
	}
	return elev.ActorID
}

func registerEODDayX(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("POST /reports/eod/x-report", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		pin, ok := dayXParse(w, r)
		if !ok {
			return
		}
		elev := checkOrElevate(d, r, "eod_report", pin)
		if elev.Outcome == needsElevation {
			dayXElevate(w, r, elev, eodDayXViewPath, "#eod-day-x-body", "elevation.summary.eod_day_x")
			return
		}
		rep, err := readEODDayX(ctx, d, eodActorName(ctx, d, dayXActor(elev)))
		if err != nil {
			writeDayXError(w, r, http.StatusInternalServerError, "x_report_failed", "reports.eod.day_x.failed", err)
			return
		}
		if wantsJSON(r) {
			writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
				"kind": "day_x", "from": rep.From, "to": rep.To, "report": rep,
			}, "error": nil})
			return
		}
		cfg := printerConfig(ctx, d)
		articleMode, articleCap := resolveEODArticlePrintSettings(ctx, d)
		doc := buildEODDayXDoc(rep, storeNameOrDefault(ctx, d), cfg.Charset, articleMode, articleCap)
		httpx.RenderPartial("ui/partials/reports_eod_day_x.html", map[string]any{
			"Preview": print.RenderText(doc),
			"From":    rep.From,
			"To":      rep.To,
			"Empty": rep.SalesCount == 0 && rep.RefundCount == 0 && rep.CancelCount == 0 &&
				rep.ClearedBasketCount == 0 && rep.NoSaleCount == 0,
		})(w, r)
	})

	mux.HandleFunc("POST /api/reports/eod/x-report/print", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		pin, ok := dayXParse(w, r)
		if !ok {
			return
		}
		elev := checkOrElevate(d, r, "eod_report", pin)
		if elev.Outcome == needsElevation {
			dayXElevate(w, r, elev, eodDayXPrintPath, "#eod-day-x-print-msg", "elevation.summary.eod_day_x_print")
			return
		}
		actorID := dayXActor(elev)
		// Fresh read: the paper shows "as of now", not the on-screen figures.
		rep, err := readEODDayX(ctx, d, eodActorName(ctx, d, actorID))
		if err != nil {
			writeDayXError(w, r, http.StatusInternalServerError, "x_report_failed", "reports.eod.day_x.failed", err)
			return
		}
		// print.PrintDoc returns nil for mode off: refuse it here, or it
		// would look like success.
		cfg, cerr := printerConfigChecked(ctx, d)
		if cerr != nil || !cfg.Enabled() {
			writeDayXError(w, r, http.StatusBadGateway, "print_failed", "reports.eod.day_x.print_failed", cerr)
			return
		}
		articleMode, articleCap := resolveEODArticlePrintSettings(ctx, d)
		doc := buildEODDayXDoc(rep, storeNameOrDefault(ctx, d), cfg.Charset, articleMode, articleCap)
		pctx, cancel := context.WithTimeout(ctx, xPrintTimeout)
		defer cancel()
		if perr := print.PrintDoc(pctx, cfg, doc); perr != nil {
			writeDayXError(w, r, http.StatusBadGateway, "print_failed", "reports.eod.day_x.print_failed", perr)
			return
		}
		// Audit after a successful print only; a failed audit write is
		// logged but still answers success (a 5xx would invite a second print).
		repo := data.NewPOSRepo(d.Db)
		actx := context.WithoutCancel(ctx)
		now := time.Now().UTC().Format(time.RFC3339)
		payload := map[string]any{"from": rep.From, "to": rep.To, "sales": rep.SalesCount, "net": rep.Net}
		var aerr error
		if elev.Outcome == elevated {
			aerr = repo.InsertAuditElevated(actx, nil, actorID, elev.ActorID, "report", rep.To, "x_day_report_printed", payload, now, "")
		} else {
			aerr = repo.InsertAudit(actx, nil, actorID, "report", rep.To, "x_day_report_printed", payload, now, "")
		}
		if aerr != nil {
			logging.L().Errorf("day x print: audit: %v", aerr)
		}
		if wantsJSON(r) {
			writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"printed": true, "to": rep.To}, "error": nil})
			return
		}
		// No X-UT-Response: ok header (it would refresh the tab and wipe the preview).
		writeHTML(w, http.StatusOK, "<span class='x-report-print-ok' role='status'>✓ "+
			html.EscapeString(httpx.T(httpx.ResolveLocale(w, r), "reports.eod.day_x.print_ok"))+"</span>")
	})
}

const (
	eodDayXViewPath  = "/reports/eod/x-report"
	eodDayXPrintPath = "/api/reports/eod/x-report/print"
)

// dayXParse caps and parses the form and returns the PIN, read from PostForm
// only: a PIN in the query string is ignored.
func dayXParse(w http.ResponseWriter, r *http.Request) (pin string, ok bool) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		writeDayXError(w, r, http.StatusBadRequest, "invalid_request", "reports.eod.day_x.invalid_request", nil)
		return "", false
	}
	return strings.TrimSpace(r.PostForm.Get("override_pin")), true
}

func dayXElevate(w http.ResponseWriter, r *http.Request, elev elevationCheck, path, target, summaryKey string) {
	if wantsJSON(r) {
		status := http.StatusForbidden
		if errors.Is(elev.Err, auth.ErrLockedOut) {
			status = http.StatusTooManyRequests
		}
		writeDayXError(w, r, status, "manager_pin_required", "common.error.manager_or_admin_required", nil)
		return
	}
	locale := httpx.ResolveLocale(w, r)
	renderElevationPrompt(w, r, path, target, httpx.T(locale, summaryKey), nil, elev)
}

// writeDayXError mirrors writeXReportError with the day-X message keys.
func writeDayXError(w http.ResponseWriter, r *http.Request, status int, code, key string, cause error) {
	if cause != nil && status >= http.StatusInternalServerError {
		logging.L().Errorf("day x report: %s: %v", code, cause)
	}
	msg := httpx.T(httpx.ResolveLocale(w, r), key)
	if wantsJSON(r) {
		writeJSON(w, status, map[string]any{"data": nil, "error": map[string]string{"code": code, "message": msg}})
		return
	}
	writeHTML(w, status, "<div class='error' role='alert'>"+html.EscapeString(msg)+"</div>")
}
