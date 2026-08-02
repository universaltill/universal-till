package pages

import (
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"regexp"
	"strings"
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
)

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

// StartEODScheduler runs the background end-of-day loop (docs: G30). Lives
// in pages because the printer/settings plumbing is here.
func StartEODScheduler(ctx context.Context, d *common.Deps) {
	go func() {
		repo := data.NewPOSRepo(d.Db)
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

	// Schedule settings (manager): enable + local time.
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
		_ = d.Settings.Set(r.Context(), keyEODEnabled, fmt.Sprintf("%t", enabled))
		_ = d.Settings.Set(r.Context(), keyEODTime, hhmm)
		w.WriteHeader(http.StatusNoContent)
	})
}
