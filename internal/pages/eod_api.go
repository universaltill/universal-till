package pages

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/print"
)

// End-of-day report (docs: architecture/end-of-day-report.md, G30).
const (
	keyEODEnabled = "reports.eod_enabled"
	keyEODTime    = "reports.eod_time" // local "HH:MM"
	// keyReportsBusinessDayStart is the per-till business-day boundary used
	// by /reports' calendar-aligned period picker (ut-docs#519):
	// day/week/month/year periods cross over at this local "HH:MM" instead
	// of naive midnight, so a bar (or any shop trading past midnight) can
	// push it later and keep one night's takings on a single report day.
	// Empty/unset defaults to "00:00" (see parseBusinessDayStart in
	// reports_page.go) — calendar-midnight behavior, unchanged from before
	// this setting existed. Same settings panel/endpoint as the EOD
	// schedule below (POST /api/settings/eod), not a new settings page.
	keyReportsBusinessDayStart = "reports.business_day_start"
)

// eodTimeRe validates any local "HH:MM" setting this file's settings panel
// stores — the EOD schedule time AND (ut-docs#519) the business-day-start
// boundary share it rather than duplicating the pattern.
var eodTimeRe = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)

// eodDateRe guards /api/reports/eod/range's from/to params. Without this, a
// non-YYYY-MM-DD value (e.g. an un-padded "2026-1-1", or garbage) still
// passes the `from > to` string check — SQLite's BETWEEN then compares that
// raw text against date(created_at) and either silently matches nothing or
// silently widens the range, handing back a financially wrong Z-report with
// a 200 and no error (2026-08-02 review finding). It also keeps the
// downloaded filename, which embeds from/to verbatim, free of path
// separators.
var eodDateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// buildEODDoc renders the Z-report for the receipt printer.
func buildEODDoc(rep data.EODReport, storeName, charset string) print.Doc {
	money := func(minor int64) string { return httpx.FormatMoney(minor, "en") }
	doc := print.Doc{
		StoreName: storeName,
		Meta: []string{
			"END OF DAY " + rep.Day,
			"Generated " + rep.GeneratedAt,
		},
		Charset: charset,
	}
	doc.Totals = []print.KV{
		{Label: fmt.Sprintf("Sales (%d)", rep.SalesCount), Amount: money(rep.Gross)},
		{Label: fmt.Sprintf("Refunds (%d)", rep.RefundCount), Amount: "-" + money(rep.RefundTotal)},
		{Label: "Tax (net)", Amount: money(rep.TaxNet)},
		{Label: "NET", Amount: money(rep.Net), Strong: true},
	}
	for _, m := range rep.Methods {
		label := strings.ToUpper(m.Method[:1]) + m.Method[1:]
		doc.Payments = append(doc.Payments, print.KV{Label: label, Amount: money(m.In - m.Out)})
	}
	// Department breakdown (E1b) + per-register breakdown, as footer lines so
	// they print on the Z-report without depending on new Doc fields.
	if len(rep.Departments) > 0 {
		doc.Footer = append(doc.Footer, "", "BY DEPARTMENT")
		for _, d := range rep.Departments {
			name := d.Department
			if name == "" {
				name = "Uncategorized"
			}
			doc.Footer = append(doc.Footer, fmt.Sprintf("%-20s %s", name, money(d.Revenue)))
		}
	}
	if len(rep.Tills) > 0 {
		doc.Footer = append(doc.Footer, "", "BY TILL")
		for _, t := range rep.Tills {
			name := t.Name
			if name == "" {
				name = "This till"
			}
			doc.Footer = append(doc.Footer, fmt.Sprintf("%-20s %s", name, money(t.Revenue)))
		}
	}
	if rep.FirstReceipt != "" {
		doc.Footer = append(doc.Footer, "", "Receipts "+rep.FirstReceipt+" - "+rep.LastReceipt)
	}
	return doc
}

// generateEOD produces, archives and (best-effort) prints the day's report.
// Idempotent per day: returns createdNew=false when already archived.
func generateEOD(ctx context.Context, d *common.Deps, day string) (data.EODReport, bool, error) {
	repo := data.NewPOSRepo(d.Db)
	rep, err := repo.EndOfDay(ctx, day)
	if err != nil {
		return rep, false, err
	}
	raw, err := json.Marshal(rep)
	if err != nil {
		return rep, false, err
	}
	created, err := repo.ArchiveReport(ctx, "eod", day, raw)
	if err != nil || !created {
		return rep, created, err
	}
	_ = repo.InsertAudit(ctx, nil, "system", "report", day, "eod_generated",
		map[string]any{"net": rep.Net, "sales": rep.SalesCount},
		time.Now().UTC().Format(time.RFC3339), "")
	if cfg := printerConfig(ctx, d); cfg.Enabled() {
		doc := buildEODDoc(rep, storeNameOrDefault(ctx, d), cfg.Charset)
		pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		if perr := print.PrintDoc(pctx, cfg, doc); perr != nil {
			logging.L().Errorf("eod print: %v", perr)
		}
	}
	return rep, true, nil
}

// eodDue is the pure schedule decision: run when enabled, past the
// configured local time, and today's report doesn't exist yet — so a till
// that was off at closing time catches up at next boot.
func eodDue(now time.Time, enabled bool, hhmm string, alreadyDone bool) bool {
	if !enabled || alreadyDone || !eodTimeRe.MatchString(hhmm) {
		return false
	}
	nowHM := now.Format("15:04")
	return nowHM >= hhmm
}

// reportPruneDue is the pure once-per-calendar-day gate for the
// report_archive age-based prune step hosted on StartEODScheduler's own
// tick (ADR-0040 §2, ut-docs#571 card 1). Unlike eodDue, the prune has no
// DB-backed idempotency marker of its own (there's no "already archived
// today" row to check) — StartEODScheduler's closure tracks lastPruneDay
// in-memory instead; an extra run after a process restart on the same day
// is harmless because the delete itself is naturally idempotent.
func reportPruneDue(today, lastPruneDay string) bool {
	return today != lastPruneDay
}

// reportRetentionCutoff is "now minus 10 years" formatted to match
// report_archive.period's "YYYY-MM-DD" text format (ADR-0040 §2, till mode).
func reportRetentionCutoff(now time.Time) string {
	return now.AddDate(-10, 0, 0).Format("2006-01-02")
}

// pruneReportArchive runs the till-mode age-based prune once per calendar
// day. Mode "cloud"/"both" is a pure no-op here (card 4 wires those, per
// ADR-0040's follow-up cards) — this card only implements till-mode
// pruning, and never invents a cloud delete predicate it can't actually
// honor yet. lastPruneDay is only advanced past a run that either (a) had
// nothing to do (non-till mode) or (b) actually pruned successfully — a
// transient DB error is retried on the next tick rather than silently
// skipped for the rest of the day.
func pruneReportArchive(ctx context.Context, d *common.Deps, repo *data.POSRepo, now time.Time, lastPruneDay *string) {
	today := now.Format("2006-01-02")
	if !reportPruneDue(today, *lastPruneDay) {
		return
	}
	mode, _, _ := d.Settings.Get(ctx, common.KeyReportRetentionMode)
	mode = strings.TrimSpace(mode)
	if mode == "" {
		mode = common.ReportRetentionModeTill
	}
	if mode != common.ReportRetentionModeTill {
		// cloud/both: nothing to prune yet (no cloud_acked_at is ever set by
		// this card) — mark today handled so the scheduler doesn't re-check
		// every tick, then move on.
		*lastPruneDay = today
		return
	}
	cutoff := reportRetentionCutoff(now)
	n, err := repo.PruneReportArchiveOlderThan(ctx, cutoff)
	if err != nil {
		logging.L().Errorf("report archive prune: %v", err)
		return
	}
	*lastPruneDay = today
	logging.L().Infof("report archive pruned: %d row(s) older than %s", n, cutoff)
}

// StartEODScheduler runs the background end-of-day loop (docs: G30). Lives
// in pages because the printer/settings plumbing is here. wg registers the
// loop with app.Run's shutdown drain (ut-docs#153) — the caller must pass
// bgCtx (not ctx), same requirement as StartCloudSync.
func StartEODScheduler(ctx context.Context, d *common.Deps, wg *sync.WaitGroup) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		repo := data.NewPOSRepo(d.Db)
		lastPruneDay := ""
		check := func() {
			get := func(key string) string {
				v, _, _ := d.Settings.Get(ctx, key)
				return strings.TrimSpace(v)
			}
			enabled := get(keyEODEnabled) == "true"
			hhmm := get(keyEODTime)
			day := time.Now().Format("2006-01-02")
			done, err := repo.HasArchivedReport(ctx, "eod", day)
			if err != nil {
				return
			}
			if !eodDue(time.Now(), enabled, hhmm, done) {
				return
			}
			if _, created, err := generateEOD(ctx, d, day); err != nil {
				logging.L().Errorf("eod scheduled run: %v", err)
			} else if created {
				logging.L().Infof("end-of-day report generated for %s", day)
			}
		}
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				check()
				pruneReportArchive(ctx, d, repo, time.Now(), &lastPruneDay)
			}
		}
	}()
}

// registerEODAPI mounts run-now, settings and the archive list endpoints.
func registerEODAPI(mux *http.ServeMux, d *common.Deps) {
	repo := data.NewPOSRepo(d.Db)

	mux.HandleFunc("POST /api/reports/eod/run", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			http.Error(w, "manager or admin required", http.StatusForbidden)
			return
		}
		day := time.Now().Format("2006-01-02")
		locale := httpx.ResolveLocale(w, r)
		rep, created, err := generateEOD(r.Context(), d, day)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, `<span class="muted">✗ %s</span>`, httpx.T(locale, "reports.eod.failed"))
			return
		}
		if !created {
			fmt.Fprintf(w, `<span class="muted">%s</span>`, httpx.T(locale, "reports.eod.exists"))
			return
		}
		fmt.Fprintf(w, `<span>✓ %s — %s %s</span>`, httpx.T(locale, "reports.eod.done"),
			httpx.T(locale, "reports.eod.net"), httpx.FormatMoney(rep.Net, locale))
	})

	// Reprint an archived report.
	mux.HandleFunc("POST /api/reports/eod/print/{period}", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			http.Error(w, "manager or admin required", http.StatusForbidden)
			return
		}
		period := r.PathValue("period")
		reports, err := repo.ListArchivedReports(r.Context(), 100)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		for _, a := range reports {
			if a.Kind != "eod" || a.Period != period {
				continue
			}
			var rep data.EODReport
			if err := json.Unmarshal([]byte(a.Content), &rep); err != nil {
				break
			}
			cfg := printerConfig(r.Context(), d)
			if !cfg.Enabled() {
				w.WriteHeader(http.StatusBadGateway)
				fmt.Fprintf(w, `<span class="muted">✗ %s</span>`, httpx.T(locale, "settings.printer.test_failed"))
				return
			}
			doc := buildEODDoc(rep, storeNameOrDefault(r.Context(), d), cfg.Charset)
			if perr := print.PrintDoc(r.Context(), cfg, doc); perr != nil {
				w.WriteHeader(http.StatusBadGateway)
				fmt.Fprintf(w, `<span class="muted">✗ %s</span>`, httpx.T(locale, "settings.printer.test_failed"))
				return
			}
			fmt.Fprintf(w, `<span>✓ %s</span>`, httpx.T(locale, "journal.reprinted"))
			return
		}
		http.Error(w, "report not found", http.StatusNotFound)
	})

	// Date-ranged Z-report (ut-docs#57), summary granularity, ad hoc — not
	// archived or auto-printed, unlike the scheduled single-day flow above.
	// Downloaded directly as a JSON file (Content-Disposition: attachment),
	// same precedent as GET /api/backup/download/{name}.
	mux.HandleFunc("POST /api/reports/eod/range", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			http.Error(w, "manager or admin required", http.StatusForbidden)
			return
		}
		_ = r.ParseForm()
		from := strings.TrimSpace(r.Form.Get("from"))
		to := strings.TrimSpace(r.Form.Get("to"))
		if !eodDateRe.MatchString(from) || !eodDateRe.MatchString(to) {
			http.Error(w, "from and to must be YYYY-MM-DD", http.StatusBadRequest)
			return
		}
		if from > to {
			http.Error(w, "from must not be after to", http.StatusBadRequest)
			return
		}
		rep, err := repo.EndOfDayRange(r.Context(), from, to)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		raw, err := json.Marshal(rep)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		filename := fmt.Sprintf("z-report-%s-to-%s.json", from, to)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(raw)
	})

	// Schedule settings (manager): enable + local time, plus (ut-docs#519)
	// the reports business-day-start boundary — a sibling field on this
	// same settings panel/endpoint rather than a new settings page.
	mux.HandleFunc("POST /api/settings/eod", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			http.Error(w, "manager or admin required", http.StatusForbidden)
			return
		}
		_ = r.ParseForm()
		hhmm := strings.TrimSpace(r.Form.Get("time"))
		enabled := r.Form.Get("enabled") == "on" || r.Form.Get("enabled") == "1"
		if enabled && !eodTimeRe.MatchString(hhmm) {
			http.Error(w, "time must be HH:MM", http.StatusBadRequest)
			return
		}
		bizDayStart := strings.TrimSpace(r.Form.Get("business_day_start"))
		if bizDayStart != "" && !eodTimeRe.MatchString(bizDayStart) {
			http.Error(w, "business_day_start must be HH:MM", http.StatusBadRequest)
			return
		}
		_ = d.Settings.Set(r.Context(), keyEODEnabled, fmt.Sprintf("%t", enabled))
		_ = d.Settings.Set(r.Context(), keyEODTime, hhmm)
		_ = d.Settings.Set(r.Context(), keyReportsBusinessDayStart, bizDayStart)
		w.WriteHeader(http.StatusNoContent)
	})
}

// maxReportArchiveExportRangeDays bounds POST /api/reports/archive/export's
// [from, to] span. report_archive rows are small (one JSON blob per closed
// day, per ADR-0040 §3's own capacity sizing — "tens of MB over ten
// years"), and the whole point of this export is letting a shop hand an
// auditor its full retained window in one go, so the bound here is the
// retention window itself (~10 years) rather than data_api.go's 366-day
// bound, which exists to cap a much heavier per-sale-row payload.
const maxReportArchiveExportRangeDays = 3660

const maxReportArchiveExportRange = maxReportArchiveExportRangeDays * 24 * time.Hour

// registerReportArchiveAPI mounts the report-retention-mode setting and the
// report_archive date-range export (ADR-0040 §1/§7, ut-docs#571 card 1).
// Coverage (earliest/latest/count) is read server-side into the settings
// page's own template data (settings_page.go) rather than a separate
// endpoint here, to keep the surface small.
func registerReportArchiveAPI(mux *http.ServeMux, d *common.Deps) {
	repo := data.NewPOSRepo(d.Db)

	// Mode setting: only "till" is accepted. This card (ADR-0040 card 1)
	// implements no cloud gate at all — nothing uploads or acks a report
	// yet — so silently accepting "cloud"/"both" here would tell the shop
	// their reports are protected by a mechanism that doesn't exist. The
	// UI renders cloud/both as visible-but-disabled for the same reason;
	// this is the server-side half of that guarantee.
	mux.HandleFunc("POST /api/settings/report-retention", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			http.Error(w, "manager or admin required", http.StatusForbidden)
			return
		}
		_ = r.ParseForm()
		mode := strings.TrimSpace(r.Form.Get("mode"))
		if mode != common.ReportRetentionModeTill {
			http.Error(w, "cloud/both report retention isn't implemented yet — only till is available", http.StatusBadRequest)
			return
		}
		if err := d.Settings.Set(r.Context(), common.KeyReportRetentionMode, mode); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// Bounded date-range export of the archive (ADR-0040 §7) — a shop
	// handing an auditor its retained reports. CSV: one row per archived
	// report (id/kind/period/created_at plus the report body as a JSON-
	// text column, keeping the shape simple rather than flattening each
	// report kind's own fields). JSON: the same rows as an array.
	mux.HandleFunc("POST /api/reports/archive/export", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			http.Error(w, "manager or admin required", http.StatusForbidden)
			return
		}
		_ = r.ParseForm()
		from := strings.TrimSpace(r.Form.Get("from"))
		to := strings.TrimSpace(r.Form.Get("to"))
		format := strings.TrimSpace(r.Form.Get("format"))
		if !eodDateRe.MatchString(from) || !eodDateRe.MatchString(to) {
			http.Error(w, "from and to must be YYYY-MM-DD", http.StatusBadRequest)
			return
		}
		if from > to {
			http.Error(w, "from must not be after to", http.StatusBadRequest)
			return
		}
		if format != "csv" && format != "json" {
			http.Error(w, "format must be csv or json", http.StatusBadRequest)
			return
		}
		fromDate, err := time.Parse("2006-01-02", from)
		if err != nil {
			http.Error(w, "from must be YYYY-MM-DD", http.StatusBadRequest)
			return
		}
		toDate, err := time.Parse("2006-01-02", to)
		if err != nil {
			http.Error(w, "to must be YYYY-MM-DD", http.StatusBadRequest)
			return
		}
		// ut-docs#229's precedent: reject an over-wide range before ever
		// touching the repo layer, same shape as data_api.go's export.
		if toDate.Sub(fromDate) > maxReportArchiveExportRange {
			http.Error(w, fmt.Sprintf("date range exceeds the %d-day maximum", maxReportArchiveExportRangeDays), http.StatusBadRequest)
			return
		}

		rows, err := repo.ArchivedReportsInRange(r.Context(), from, to)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if format == "json" {
			filename := fmt.Sprintf("report-archive-%s-to-%s.json", from, to)
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(rows)
			return
		}

		filename := fmt.Sprintf("report-archive-%s-to-%s.csv", from, to)
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
		w.WriteHeader(http.StatusOK)
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"id", "kind", "period", "created_at", "content_json"})
		for _, row := range rows {
			_ = cw.Write([]string{row.ID, row.Kind, row.Period, row.CreatedAt, row.Content})
		}
		cw.Flush()
	})
}
