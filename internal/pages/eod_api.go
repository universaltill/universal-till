package pages

import (
	"context"
	"encoding/json"
	"fmt"
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
	if rep.FirstReceipt != "" {
		doc.Footer = []string{"Receipts " + rep.FirstReceipt + " - " + rep.LastReceipt}
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
		if tr, err := print.NewTransport(cfg); err == nil && tr != nil {
			doc := buildEODDoc(rep, storeNameOrDefault(ctx, d), cfg.Charset)
			pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			if perr := tr.Print(pctx, print.Render(doc)); perr != nil {
				logging.L().Errorf("eod print: %v", perr)
			}
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
			tr, terr := print.NewTransport(cfg)
			if terr != nil || tr == nil {
				w.WriteHeader(http.StatusBadGateway)
				fmt.Fprintf(w, `<span class="muted">✗ %s</span>`, httpx.T(locale, "settings.printer.test_failed"))
				return
			}
			doc := buildEODDoc(rep, storeNameOrDefault(r.Context(), d), cfg.Charset)
			if perr := tr.Print(r.Context(), print.Render(doc)); perr != nil {
				w.WriteHeader(http.StatusBadGateway)
				fmt.Fprintf(w, `<span class="muted">✗ %s</span>`, httpx.T(locale, "settings.printer.test_failed"))
				return
			}
			fmt.Fprintf(w, `<span>✓ %s</span>`, httpx.T(locale, "journal.reprinted"))
			return
		}
		http.Error(w, "report not found", http.StatusNotFound)
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
