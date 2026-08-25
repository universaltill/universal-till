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

// fmtRateBP renders a basis-point tax rate as a percentage label for the
// Z-report's VAT band footer: 700 → "7%", 1900 → "19%", and a non-whole
// rate like 1050 → "10.5%" (trailing zeros trimmed, integer arithmetic
// only — no floats near money-adjacent figures). Defensive on negatives:
// nothing in the codebase validates tax_rate_bp >= 0, and the naive
// "%d.%02d" on e.g. -50 rendered the garbage "0.-5%" — so the sign is
// pulled out and the digits formatted from the absolute value instead of
// assuming non-negative input.
func fmtRateBP(bp int) string {
	sign := ""
	if bp < 0 {
		sign, bp = "-", -bp
	}
	if bp%100 == 0 {
		return fmt.Sprintf("%s%d%%", sign, bp/100)
	}
	return sign + strings.TrimRight(fmt.Sprintf("%d.%02d", bp/100, bp%100), "0") + "%"
}

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
	// Tips by payment method (ut-docs#1007), as footer lines rather than
	// folded into doc.Payments above -- the reference day-close reports
	// tips held OUT of revenue, never mixed into the payment-method
	// takings those KV rows represent, and this section only appears at
	// all once there's at least one tipped payment for the period (an
	// empty rep.Tips -- e.g. a terminal with tipping disabled and zero
	// cash tips -- prints nothing, same convention Departments/Tills
	// below already follow).
	if len(rep.Tips) > 0 {
		doc.Footer = append(doc.Footer, "", "TIPS (held out of revenue)")
		var tipTotal int64
		for _, tp := range rep.Tips {
			label := strings.ToUpper(tp.Method[:1]) + tp.Method[1:]
			doc.Footer = append(doc.Footer, fmt.Sprintf("%-20s %s", fmt.Sprintf("%dx %s", tp.Count, label), money(tp.Amount)))
			tipTotal += tp.Amount
		}
		doc.Footer = append(doc.Footer, fmt.Sprintf("%-20s %s", "TOTAL", money(tipTotal)))
	}
	// Per-VAT-rate breakdown (ut-docs#1003) + department breakdown (E1b) +
	// per-register breakdown, as footer lines so they print on the Z-report
	// without depending on new Doc fields.
	if len(rep.TaxBands) > 0 {
		// Column widths: start from the historical fixed layout (6/11/10/11
		// = 41 chars incl. separators, print.Width is 42) and grow any
		// column whose largest value doesn't fit. If the grown row would
		// exceed print.Width — where the ESC/POS renderer hard-clips footer
		// lines, silently amputating the gross's trailing digits on a
		// £1,000,000.00+ day (2026-08-25 review finding) — collapse to
		// tight widths (each column exactly its widest cell) instead, which
		// keeps every digit printable up to ~£1M in all three money columns
		// at once before physics wins.
		rate, net, tax, gross := make([]string, len(rep.TaxBands)), make([]string, len(rep.TaxBands)), make([]string, len(rep.TaxBands)), make([]string, len(rep.TaxBands))
		tw := [4]int{0, len("Net"), len("Tax"), len("Gross")} // tight: header floors
		for i, b := range rep.TaxBands {
			rate[i], net[i], tax[i], gross[i] = fmtRateBP(b.RateBP), money(b.Net), money(b.Tax), money(b.Gross)
			for j, s := range []string{rate[i], net[i], tax[i], gross[i]} {
				if len(s) > tw[j] {
					tw[j] = len(s)
				}
			}
		}
		w := tw
		for j, min := range [4]int{6, 11, 10, 11} { // historical layout when it fits
			if w[j] < min {
				w[j] = min
			}
		}
		if w[0]+w[1]+w[2]+w[3]+3 > print.Width {
			w = tw
		}
		doc.Footer = append(doc.Footer, "", "BY VAT RATE")
		doc.Footer = append(doc.Footer, fmt.Sprintf("%-*s %*s %*s %*s", w[0], "", w[1], "Net", w[2], "Tax", w[3], "Gross"))
		for i := range rep.TaxBands {
			doc.Footer = append(doc.Footer, fmt.Sprintf("%-*s %*s %*s %*s",
				w[0], rate[i], w[1], net[i], w[2], tax[i], w[3], gross[i]))
		}
	}
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
	// Voucher liability flows (ut-docs#1008) — their own footer section,
	// SEPARATE from and never summed into the article/department figures: an
	// issue is a 0% liability (inside the day's overall total, outside
	// Artikelumsatz and every per-rate VAT band), a redemption is a payment
	// method. Same footer-line precedent as BY DEPARTMENT / BY TILL above;
	// "GUTSCHEINE" matches the Germany-pilot Z-report vocabulary the card
	// specifies. Omitted entirely on a day with no voucher activity.
	if rep.VouchersIssuedCount > 0 || rep.VouchersRedeemedCount > 0 {
		doc.Footer = append(doc.Footer, "", "GUTSCHEINE")
		doc.Footer = append(doc.Footer, fmt.Sprintf("%-20s %s", fmt.Sprintf("Issued (%d)", rep.VouchersIssuedCount), money(rep.VouchersIssued)))
		doc.Footer = append(doc.Footer, fmt.Sprintf("%-20s %s", fmt.Sprintf("Redeemed (%d)", rep.VouchersRedeemedCount), money(rep.VouchersRedeemed)))
	}
	// Cash-drawer reconciliation (ut-docs#1006) — printed only when at
	// least one shift was closed that day; a day-close without one still
	// renders a complete report. Amounts stored negative (skim, pay-outs)
	// print sign-first ("-£411.10"), same convention as the Refunds line.
	if rc := rep.CashReconciliation; rc != nil {
		signed := func(minor int64) string {
			if minor < 0 {
				return "-" + money(-minor)
			}
			return money(minor)
		}
		line := func(label string, amount string) string {
			return fmt.Sprintf("%-20s %s", label, amount)
		}
		varianceLine := line("Variance", signed(rc.Variance))
		if rc.Variance != 0 {
			// Flag a count discrepancy so it can't be missed on paper.
			varianceLine += " !!"
		}
		doc.Footer = append(doc.Footer, "", "CASH RECONCILIATION",
			line("Opening float", signed(rc.OpeningFloat)),
			line("Cash sales", signed(rc.CashSales)),
			line("Pay-ins", signed(rc.PayIns)),
			line("Pay-outs", signed(rc.PayOuts)),
			line("Calculated", signed(rc.Calculated)),
			line("Counted", signed(rc.Counted)),
			varianceLine,
			line("Skim to safe", signed(rc.Skim)),
			line("New float", signed(rc.NewFloat)),
		)
	}
	if rep.FirstReceipt != "" {
		doc.Footer = append(doc.Footer, "", "Receipts "+rep.FirstReceipt+" - "+rep.LastReceipt)
	}
	return doc
}

// generateEOD produces, archives and (best-effort) prints the day's report.
// Idempotent per day: returns createdNew=false when already archived.
//
// actor/blockedActorID (ut-docs#794) let the two very different callers
// each get the audit attribution they need from the SAME function, rather
// than forking generateEOD in two: StartEODScheduler's unattended ticker
// passes ("system", "") — unchanged from before this card, no elevation
// involved, never prompts. POST /api/reports/eod/run runs checkOrElevate
// FIRST and passes the resolved actor (session user, or the approver once
// elevated) plus blockedActorID (the originally-blocked session user, set
// only when elevated) so the eod_generated entry gets the same dual
// attribution every other checkOrElevate site already writes.
func generateEOD(ctx context.Context, d *common.Deps, day, actor, blockedActorID string) (data.EODReport, bool, error) {
	repo := data.NewPOSRepo(d.Db)
	rep, err := repo.EndOfDay(ctx, day)
	if err != nil {
		return rep, false, err
	}
	// Per-VAT-rate breakdown (ut-docs#1003) — computed here, not inside
	// EndOfDay, because the banding math needs internal/pos (see
	// eod_tax_bands.go). Must succeed BEFORE the report is archived: the
	// archive is write-once per day, so a report archived without its VAT
	// table could never be repaired.
	if err := attachEODTaxBands(ctx, repo, &rep); err != nil {
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
	payload := map[string]any{"net": rep.Net, "sales": rep.SalesCount}
	now := time.Now().UTC().Format(time.RFC3339)
	if blockedActorID != "" {
		_ = repo.InsertAuditElevated(ctx, nil, actor, blockedActorID, "report", day, "eod_generated", payload, now, "")
	} else {
		_ = repo.InsertAudit(ctx, nil, actor, "report", day, "eod_generated", payload, now, "")
	}
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

// reportRetentionCutoff is "now minus 10 calendar years" formatted to match
// report_archive.period's "YYYY-MM-DD" text format (ADR-0040 §2, till mode).
// Deliberately calendar years (AddDate), not a fixed day count: 10 calendar
// years always spans at least data.GlobalArchiveMinDays (3650) days — every
// 10-year span contains 2 or 3 leap days, so this window is always a few
// days *more* generous than the country_settings floor, never less.
// TestReportRetentionCutoffNeverShorterThanGlobalArchiveMinDays (below)
// pins that relationship so the two can't silently drift the other way
// (ut-docs#659 review finding B2 — GlobalArchiveMinDays and this cutoff
// used to be two independently-spelled "10 years" with nothing tying them
// together).
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
	if n > 0 {
		// This permanently destroys rows ADR-0040 itself designates a
		// retained legal record -- unlike a manager-triggered action (e.g.
		// ResetTransactionHistory), it's fully automatic, so the audit
		// trail is the only record that it happened at all. Best-effort:
		// the rows are already gone either way, so a failed audit write
		// logs but doesn't retry/block the scheduler.
		if err := repo.InsertAudit(ctx, nil, "system", "report_archive", cutoff, "report_archive_pruned",
			map[string]any{"rows_deleted": n, "cutoff": cutoff}, time.Now().UTC().Format(time.RFC3339), ""); err != nil {
			logging.L().Errorf("report archive prune: audit write failed: %v", err)
		}
	}
}

// eodSchedulerTick is StartEODScheduler's per-tick body, pulled out to a
// package-level function (ut-docs#794 review — same shape as
// pruneReportArchive below) so a test can drive the REAL scheduler code
// path directly instead of only proving generateEOD behaves given
// test-supplied ("system", "") args. Unattended by construction: there is
// no *http.Request here, so it's never reachable through checkOrElevate —
// generateEOD is always called with the literal ("system", "") actor pair.
func eodSchedulerTick(ctx context.Context, d *common.Deps, repo *data.POSRepo) {
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
	if _, created, err := generateEOD(ctx, d, day, "system", ""); err != nil {
		logging.L().Errorf("eod scheduled run: %v", err)
	} else if created {
		logging.L().Infof("end-of-day report generated for %s", day)
	}
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
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				eodSchedulerTick(ctx, d, repo)
				pruneReportArchive(ctx, d, repo, time.Now(), &lastPruneDay)
			}
		}
	}()
}

// registerEODAPI mounts run-now, settings and the archive list endpoints.
//
// ut-docs#794 / ADR-0052 §2 scope note: the ADR originally scoped
// checkOrElevate to handlers that ALREADY wrote an audit entry for their
// action — only POST /api/reports/eod/run met that bar (via generateEOD's
// own InsertAudit). The other 5 eod_report sites here (print/{period},
// range, and — in registerReportArchiveAPI below — report-retention and
// archive/export) wrote NO audit entry before this card, and 2 of those 5
// (range, archive/export) are read-only exports rather than state
// mutations, which §2 also carves out ("mutating ... actions only").
// Judgment call made here, not a formal ADR amendment: this card's own
// text named all 6 as in scope, an export still leaves a real trail of
// which financial data left the shop and to whom, and wiring elevation
// onto a site without also giving it the audit write §2 requires would be
// a silent, undetectable gap — so all 5 get the same
// checkOrElevate/InsertAuditElevated treatment as the 3 already-shipped
// sites, rather than leaving 5 of this card's own 6 named sites
// unimplemented. Mirrors migration 042's own precedent of recording a
// judgment call inline rather than blocking on a new ADR for it.

func registerEODAPI(mux *http.ServeMux, d *common.Deps) {
	repo := data.NewPOSRepo(d.Db)

	mux.HandleFunc("POST /api/reports/eod/run", func(w http.ResponseWriter, r *http.Request) {
		// Mutating + audit-writing (ut-docs#557/#794): a denied session gets
		// an in-place PIN re-auth instead of a flat 403. generateEOD's own
		// audit write (inside it, not here) is what actually needs the
		// resolved actor/blockedActorID — see its doc comment.
		elev := checkOrElevate(d, r, "eod_report", r.FormValue("override_pin"))
		if elev.Outcome == needsElevation {
			locale := httpx.ResolveLocale(w, r)
			renderElevationPrompt(w, r, "/api/reports/eod/run", "#eod-msg",
				httpx.T(locale, "elevation.summary.eod_run"), nil, elev)
			return
		}
		actorID := elev.ActorID
		blockedActorID := ""
		if elev.Outcome == elevated {
			actorID = elev.ApproverID
			blockedActorID = elev.ActorID
		}
		day := time.Now().Format("2006-01-02")
		locale := httpx.ResolveLocale(w, r)
		rep, created, err := generateEOD(r.Context(), d, day, actorID, blockedActorID)
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
		period := r.PathValue("period")
		// ut-docs#794 review finding (nit): confirm the period actually
		// exists BEFORE elevating — sync_api.go's own precedent (cited
		// elsewhere in this file) is "don't burn a PIN entry/shared-device
		// lockout slot on a request that's refused either way."
		has, err := repo.HasArchivedReport(r.Context(), "eod", period)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "eod.err.check_failed", "eod_print_check", err)
			return
		}
		if !has {
			http.Error(w, "report not found", http.StatusNotFound)
			return
		}
		// Mutating + audit-writing (ut-docs#794): no extra form fields to
		// replay on retry — the dialog re-POSTs to r.URL.Path, which already
		// carries {period} — so no Hidden entries needed, unlike the sites
		// below whose fields live in the request body.
		elev := checkOrElevate(d, r, "eod_report", r.FormValue("override_pin"))
		if elev.Outcome == needsElevation {
			locale := httpx.ResolveLocale(w, r)
			renderElevationPrompt(w, r, r.URL.Path, "#eod-msg",
				fmt.Sprintf(httpx.T(locale, "elevation.summary.eod_reprint"), period), nil, elev)
			return
		}
		actorID := elev.ActorID
		if elev.Outcome == elevated {
			actorID = elev.ApproverID
		}
		reports, err := repo.ListArchivedReports(r.Context(), 100)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "eod.err.list_failed", "eod_print_list", err)
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
			now := time.Now().UTC().Format(time.RFC3339)
			if elev.Outcome == elevated {
				_ = repo.InsertAuditElevated(r.Context(), nil, actorID, elev.ActorID, "report", period, "eod_reprinted", nil, now, "")
			} else {
				_ = repo.InsertAudit(r.Context(), nil, actorID, "report", period, "eod_reprinted", nil, now, "")
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
		// Mutating + audit-writing (ut-docs#794): the SAME checkOrElevate
		// gate as every other site here, but this handler's caller is raw
		// fetch(), not htmx (it needs Content-Disposition-triggered blob
		// download, which htmx can't do) — see app.js's
		// utPostWithElevation, which detects this HTML response by
		// Content-Type (renderElevationPrompt sets it explicitly for
		// exactly this reason) and drives the SAME shared dialog manually.
		elev := checkOrElevate(d, r, "eod_report", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			renderElevationPrompt(w, r, "/api/reports/eod/range", "#eod-range-msg",
				fmt.Sprintf(httpx.T(httpx.ResolveLocale(w, r), "elevation.summary.eod_range_export"), from, to),
				[]elevationHiddenField{{Name: "from", Value: from}, {Name: "to", Value: to}}, elev)
			return
		}
		actorID := elev.ActorID
		if elev.Outcome == elevated {
			actorID = elev.ApproverID
		}
		rep, err := repo.EndOfDayRange(r.Context(), from, to)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "eod.err.range_failed", "eod_range_query", err)
			return
		}
		// Per-VAT-rate breakdown (ut-docs#1003), same as generateEOD's
		// single-day flow — see eod_tax_bands.go for why it's not inside
		// EndOfDayRange itself.
		if err := attachEODTaxBands(r.Context(), repo, &rep); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "eod.err.range_failed", "eod_range_bands", err)
			return
		}
		raw, err := json.Marshal(rep)
		if err != nil {
			// Not provably unreachable, so it is handled rather than ignored:
			// EODReport (pos_repo.go) carries no chan/func/cyclic pointer, but
			// DeptSales.Qty IS a float64 (SUM over sale_lines.quantity, a REAL
			// column), and json.Marshal rejects a non-finite float outright
			// ("json: unsupported value: +Inf"). Departments is populated
			// whenever from == to, which a single-day range export does.
			// Reaching this therefore needs a non-finite quantity to already be
			// persisted; nothing on the write path currently rejects one (see
			// the follow-up card from the ut-docs#945 review), so there is no
			// forced-failure test here yet -- the guard exists precisely so
			// that day surfaces a translated message, not a raw encoder error.
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "eod.err.range_failed", "eod_range_encode", err)
			return
		}
		now := time.Now().UTC().Format(time.RFC3339)
		payload := map[string]any{"from": from, "to": to}
		if elev.Outcome == elevated {
			_ = repo.InsertAuditElevated(r.Context(), nil, actorID, elev.ActorID, "report", from+"_to_"+to, "eod_range_exported", payload, now, "")
		} else {
			_ = repo.InsertAudit(r.Context(), nil, actorID, "report", from+"_to_"+to, "eod_range_exported", payload, now, "")
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
		_ = r.ParseForm()
		hhmm := strings.TrimSpace(r.Form.Get("time"))
		enabledRaw := r.Form.Get("enabled")
		enabled := enabledRaw == "on" || enabledRaw == "1"
		if enabled && !eodTimeRe.MatchString(hhmm) {
			http.Error(w, "time must be HH:MM", http.StatusBadRequest)
			return
		}
		bizDayStart := strings.TrimSpace(r.Form.Get("business_day_start"))
		if bizDayStart != "" && !eodTimeRe.MatchString(bizDayStart) {
			http.Error(w, "business_day_start must be HH:MM", http.StatusBadRequest)
			return
		}
		// Mutating + audit-writing (ut-docs#794): validated above (sync_api.go's
		// precedent — don't burn a PIN entry on a request that 400s either
		// way), gated below. Hidden replays the other two fields so the
		// dialog's retry re-submits the SAME settings, not blank ones —
		// this form has hx-swap="none" (no visible hx-target of its own),
		// so HxTarget here points at a dedicated message span
		// (#eod-settings-msg, reports_tab_eod.html) that exists purely for
		// the elevation retry's response to land in.
		elev := checkOrElevate(d, r, "eod_report", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			locale := httpx.ResolveLocale(w, r)
			// ut-docs#794 review finding (should-fix): the summary must
			// name the actual value being approved, not just "the schedule
			// changed" — silently turning OFF the automatic Z-report is the
			// concrete risk a static summary would hide from the approver
			// (ADR-0052 §3).
			summary := httpx.T(locale, "elevation.summary.eod_settings_disabled")
			if enabled {
				summary = fmt.Sprintf(httpx.T(locale, "elevation.summary.eod_settings_enabled"), hhmm)
			}
			renderElevationPrompt(w, r, "/api/settings/eod", "#eod-settings-msg", summary,
				[]elevationHiddenField{
					{Name: "enabled", Value: enabledRaw},
					{Name: "time", Value: hhmm},
					{Name: "business_day_start", Value: bizDayStart},
				}, elev)
			return
		}
		actorID := elev.ActorID
		if elev.Outcome == elevated {
			actorID = elev.ApproverID
		}
		_ = d.Settings.Set(r.Context(), keyEODEnabled, fmt.Sprintf("%t", enabled))
		_ = d.Settings.Set(r.Context(), keyEODTime, hhmm)
		_ = d.Settings.Set(r.Context(), keyReportsBusinessDayStart, bizDayStart)
		now := time.Now().UTC().Format(time.RFC3339)
		payload := map[string]any{"enabled": enabled, "time": hhmm, "business_day_start": bizDayStart}
		if elev.Outcome == elevated {
			_ = repo.InsertAuditElevated(r.Context(), nil, actorID, elev.ActorID, "report", "-", "eod_settings_changed", payload, now, "")
			// ut-docs#794 review finding (should-fix): a 204 never swaps at
			// all under htmx (not even the dialog retry's own OOB content),
			// so the plain-session path below keeps its existing 204 +
			// page-managed reload, but the elevation dialog's retry — which
			// has no reload of its own — needs a real body landing in
			// #eod-settings-msg or the approver gets zero confirmation that
			// anything happened.
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprintf(w, `<span>✓ %s</span>`, httpx.T(httpx.ResolveLocale(w, r), "elevation.approved"))
			return
		}
		_ = repo.InsertAudit(r.Context(), nil, actorID, "report", "-", "eod_settings_changed", payload, now, "")
		w.WriteHeader(http.StatusNoContent)
	})
}

// maxReportArchiveExportRangeDays bounds POST /api/reports/archive/export's
// [from, to] span. report_archive rows are small (one JSON blob per closed
// day, per ADR-0040 §3's own capacity sizing — "tens of MB over ten
// years"), and the whole point of this export is letting a shop hand an
// auditor its full retained window in one go, so the bound here is the
// retention floor (data.GlobalArchiveMinDays — the same single source of
// truth reportRetentionCutoff above and the country_settings floor both
// use) plus 10 days' slack, rather than data_api.go's 366-day bound, which
// exists to cap a much heavier per-sale-row payload.
const maxReportArchiveExportRangeDays = data.GlobalArchiveMinDays + 10

const maxReportArchiveExportRange = time.Duration(maxReportArchiveExportRangeDays) * 24 * time.Hour

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
		_ = r.ParseForm()
		mode := strings.TrimSpace(r.Form.Get("mode"))
		if mode != common.ReportRetentionModeTill {
			http.Error(w, "cloud/both report retention isn't implemented yet — only till is available", http.StatusBadRequest)
			return
		}
		// Mutating + audit-writing (ut-docs#794): validated above, gated
		// below, same as every other site in this file. hx-swap="none" here
		// too (settings.html reloads the page itself on success via
		// hx-on::after-request) — HxTarget points at a dedicated
		// #retention-msg span added purely for the elevation retry.
		elev := checkOrElevate(d, r, "eod_report", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			locale := httpx.ResolveLocale(w, r)
			modeLabel := httpx.T(locale, fmt.Sprintf("settings.retention.mode_%s", mode))
			renderElevationPrompt(w, r, "/api/settings/report-retention", "#retention-msg",
				fmt.Sprintf(httpx.T(locale, "elevation.summary.report_retention"), modeLabel),
				[]elevationHiddenField{{Name: "mode", Value: mode}}, elev)
			return
		}
		actorID := elev.ActorID
		if elev.Outcome == elevated {
			actorID = elev.ApproverID
		}
		if err := d.Settings.Set(r.Context(), common.KeyReportRetentionMode, mode); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "eod.err.retention_save_failed", "eod_retention_save", err)
			return
		}
		now := time.Now().UTC().Format(time.RFC3339)
		payload := map[string]any{"mode": mode}
		if elev.Outcome == elevated {
			_ = repo.InsertAuditElevated(r.Context(), nil, actorID, elev.ActorID, "report", "-", "report_retention_mode_changed", payload, now, "")
			// ut-docs#794 review finding (should-fix): same reasoning as
			// /api/settings/eod above — a 204 never swaps under htmx, so the
			// dialog retry (no reload of its own, unlike the plain-session
			// form below) needs a real body to confirm anything happened.
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprintf(w, `<span>✓ %s</span>`, httpx.T(httpx.ResolveLocale(w, r), "elevation.approved"))
			return
		}
		_ = repo.InsertAudit(r.Context(), nil, actorID, "report", "-", "report_retention_mode_changed", payload, now, "")
		w.WriteHeader(http.StatusNoContent)
	})

	// Bounded date-range export of the archive (ADR-0040 §7) — a shop
	// handing an auditor its retained reports. CSV: one row per archived
	// report (id/kind/period/created_at plus the report body as a JSON-
	// text column, keeping the shape simple rather than flattening each
	// report kind's own fields). JSON: the same rows as an array.
	mux.HandleFunc("POST /api/reports/archive/export", func(w http.ResponseWriter, r *http.Request) {
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

		// Mutating + audit-writing (ut-docs#794): same raw-fetch shape as
		// /api/reports/eod/range above (file download, so htmx can't drive
		// it) — see app.js's utPostWithElevation.
		elev := checkOrElevate(d, r, "eod_report", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			renderElevationPrompt(w, r, "/api/reports/archive/export", "#retention-export-msg",
				fmt.Sprintf(httpx.T(httpx.ResolveLocale(w, r), "elevation.summary.archive_export"), from, to, format),
				[]elevationHiddenField{
					{Name: "from", Value: from},
					{Name: "to", Value: to},
					{Name: "format", Value: format},
				}, elev)
			return
		}
		actorID := elev.ActorID
		if elev.Outcome == elevated {
			actorID = elev.ApproverID
		}

		rows, err := repo.ArchivedReportsInRange(r.Context(), from, to)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "eod.err.archive_export_failed", "eod_archive_export", err)
			return
		}
		now := time.Now().UTC().Format(time.RFC3339)
		payload := map[string]any{"from": from, "to": to, "format": format}
		if elev.Outcome == elevated {
			_ = repo.InsertAuditElevated(r.Context(), nil, actorID, elev.ActorID, "report", from+"_to_"+to, "report_archive_exported", payload, now, "")
		} else {
			_ = repo.InsertAudit(r.Context(), nil, actorID, "report", from+"_to_"+to, "report_archive_exported", payload, now, "")
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
		if err := cw.Error(); err != nil {
			// Headers and a 200 are already on the wire -- nothing left to
			// do for the client, but this must not vanish silently: a
			// truncated CSV under a 200 is worse than an ignored error.
			logging.L().Errorf("report archive export: csv write: %v", err)
		}
	})
}
