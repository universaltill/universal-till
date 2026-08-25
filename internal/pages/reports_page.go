package pages

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// parseReportDays reads the shared ?days= window for /reports and its tab
// fragments: 1..365, anything else (missing, garbage, out of range) falls
// back to the 14-day default rather than erroring or hammering the DB with
// an unbounded window. This stays the fallback path when no calendar
// ?period= is selected (ut-docs#519 adds the calendar-aligned path
// alongside it — parseReportWindow below — without changing this one).
func parseReportDays(r *http.Request) int {
	days := 14
	if v, err := strconv.Atoi(r.URL.Query().Get("days")); err == nil && v > 0 && v <= 365 {
		days = v
	}
	return days
}

// reportNow is "now", padded by a second so a sale inserted in the same
// instant as the request (racing the open end of a rolling/open-ended
// window) isn't dropped by an exclusive upper bound. Mirrors the pattern
// already used by alerts.go/backoffice_page.go/ask_api.go/inventory_page.go
// for their own inline from/to windows.
func reportNow() time.Time {
	return time.Now().Add(time.Second)
}

// reportPeriodLabels maps a valid ?period= value to the i18n key describing
// it, and doubles as the set of values parseReportWindow treats as
// calendar-aligned (anything else — missing, "", or garbage — falls back to
// the rolling ?days= window).
var reportPeriodLabels = map[string]string{
	"day":   "reports.period.day",
	"week":  "reports.period.week",
	"month": "reports.period.month",
	"year":  "reports.period.year",
}

// reportWindow is a resolved, ready-to-query [From, To) window plus the
// i18n label (if any) describing it, and the business-day-shifted anchor
// date ("YYYY-MM-DD") the window was resolved from. Label is "" for the
// rolling-?days= fallback path and one of reportPeriodLabels' values for a
// calendar period. Anchor is always populated (even on the rolling-?days=
// path) so the picker's date input always has a sane, business-day-correct
// default to show — callers must read Anchor from here rather than
// recomputing "today" themselves, or they drift from the boundary this
// window was actually resolved against (ut-docs#519 review finding).
// Hour/Minute are the same resolved business-day-start hh:mm used to build
// From/To (parseBusinessDayStart) — callers needing SalesByDay's grouping to
// agree with this window's own boundary (ut-docs#559) read them from here
// rather than re-parsing the setting a second time.
type reportWindow struct {
	From, To     time.Time
	Label        string
	Anchor       string
	Hour, Minute int
}

// parseBusinessDayStart parses a "reports.business_day_start" setting value
// ("HH:MM", validated by eod_api.go's eodTimeRe — the same pattern the EOD
// schedule time uses). Empty or malformed input defaults to midnight (0, 0)
// — calendar-midnight behavior, unchanged from before this setting existed.
func parseBusinessDayStart(hhmm string) (hour, minute int) {
	if hhmm == "" || !eodTimeRe.MatchString(hhmm) {
		return 0, 0
	}
	parts := strings.SplitN(hhmm, ":", 2)
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return 0, 0
	}
	return h, m
}

// businessDateFor returns the calendar date t's instant belongs to, treating
// hh:mm as the boundary between one business day and the next instead of
// naive midnight: an instant before hh:mm on its calendar day belongs to the
// PREVIOUS calendar date's business day.
func businessDateFor(t time.Time, hh, mm int) time.Time {
	loc := t.Location()
	boundary := time.Date(t.Year(), t.Month(), t.Day(), hh, mm, 0, 0, loc)
	d := t
	if t.Before(boundary) {
		d = t.AddDate(0, 0, -1)
	}
	return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc)
}

// parseReportWindow reads ?period=day|week|month|year plus an optional
// ?anchor=YYYY-MM-DD (default: now) and resolves a calendar-aligned [From,
// To) window shifted by the business-day boundary (businessDayStart, a
// "reports.business_day_start" setting value in "HH:MM" form — see
// parseBusinessDayStart). If ?period is missing or not one of the four
// known values, it falls back to the exact rolling-?days= behavior
// (parseReportDays) with no Label, so every pre-existing caller of the
// ?days= window keeps working unchanged.
func parseReportWindow(r *http.Request, businessDayStart string) reportWindow {
	hh, mm := parseBusinessDayStart(businessDayStart)
	anchorDate := businessDateFor(reportNow(), hh, mm)
	if a := r.URL.Query().Get("anchor"); a != "" {
		if parsed, err := time.ParseInLocation("2006-01-02", a, time.Local); err == nil {
			anchorDate = parsed
		}
	}
	anchorStr := anchorDate.Format("2006-01-02")

	period := r.URL.Query().Get("period")
	label, ok := reportPeriodLabels[period]
	if !ok {
		to := reportNow()
		from := to.AddDate(0, 0, -parseReportDays(r))
		return reportWindow{From: from, To: to, Anchor: anchorStr, Hour: hh, Minute: mm}
	}

	at := func(y int, m time.Month, d int) time.Time {
		return time.Date(y, m, d, hh, mm, 0, 0, time.Local)
	}

	var from, to time.Time
	switch period {
	case "day":
		from = at(anchorDate.Year(), anchorDate.Month(), anchorDate.Day())
		to = from.AddDate(0, 0, 1)
	case "week":
		// ISO-ish week: Monday through (exclusive) the following Monday.
		wd := int(anchorDate.Weekday())
		if wd == 0 {
			wd = 7 // Sunday -> end of the week, not the start of it
		}
		monday := anchorDate.AddDate(0, 0, -(wd - 1))
		from = at(monday.Year(), monday.Month(), monday.Day())
		to = from.AddDate(0, 0, 7)
	case "month":
		from = at(anchorDate.Year(), anchorDate.Month(), 1)
		to = from.AddDate(0, 1, 0)
	case "year":
		from = at(anchorDate.Year(), time.January, 1)
		to = from.AddDate(1, 0, 0)
	}

	return reportWindow{From: from, To: to, Label: label, Anchor: anchorStr, Hour: hh, Minute: mm}
}

// reportPeriodParam resolves the ?period= value /reports' picker should
// treat as selected: the raw value when it's one of the known calendar
// periods, else "" (the rolling-?days= fallback UI).
func reportPeriodParam(r *http.Request) string {
	period := r.URL.Query().Get("period")
	if _, ok := reportPeriodLabels[period]; ok {
		return period
	}
	return ""
}

func registerReportsPage(mux *http.ServeMux, d *common.Deps) {
	// GET /reports is the always-visible monitoring section only: the KPI
	// row (revenue/sales/tax/refunds/net/YoY) and the low-stock chip. The
	// heavier reports (~16 queries previously run unconditionally,
	// ut-docs#401) moved behind /ui/reports/tab/{name} and only run when
	// the operator opens that tab.
	mux.HandleFunc("/reports", func(w http.ResponseWriter, r *http.Request) {
		days := parseReportDays(r)
		bizDayStart, _, _ := d.Settings.Get(r.Context(), keyReportsBusinessDayStart)
		window := parseReportWindow(r, bizDayStart)
		repo := data.NewPOSRepo(d.Db)
		daily, _ := repo.SalesByDay(r.Context(), window.From, window.To, window.Hour, window.Minute)
		curPeriod, lastYear, _ := repo.PeriodComparison(r.Context(), window.From, window.To)
		yoyPct := 0
		if lastYear.Total > 0 {
			yoyPct = int((curPeriod.Total - lastYear.Total) * 100 / lastYear.Total)
		}
		// Low-stock heads-up (shares the inventory page's exact decision via
		// LowStockItem.IsRunningOut — this chip links straight to
		// /inventory, so it must never disagree with what that page itself
		// warns about): a chip on the reports header so the owner sees it
		// without digging. This sell-rate window is a fixed rolling 28 days
		// regardless of the selected report period/window — it feeds a
		// stock-runout prediction, not the report figures above.
		sellRateNow := reportNow()
		runningOut := 0
		if rates, err := repo.ItemDailySellRates(r.Context(), sellRateNow.Add(-28*24*time.Hour), sellRateNow); err == nil && len(rates) > 0 {
			if lvls, err := repo.ListStockLevels(r.Context()); err == nil {
				for _, l := range lvls {
					if l.IsRunningOut(rates[l.ItemID]) {
						runningOut++
					}
				}
			}
		}

		var grandTotal, grandTax int64
		var grandCount int
		for _, dd := range daily {
			grandTotal += dd.Total
			grandTax += dd.TaxTotal
			grandCount += dd.Count
		}
		grandRefunds, _, _ := repo.RefundsByWindow(r.Context(), window.From, window.To)
		grandNet := grandTotal - grandRefunds

		httpx.Render("ui/pages/reports.html", map[string]any{
			"title":        "Reports",
			"theme":        d.CurrentState().Theme,
			"menuItems":    d.MenuSnapshot(),
			"CanAsk":       aiService(r.Context(), d).CanAsk() && canPerform(d, r, "reports"),
			"IsManager":    canPerform(d, r, "reports"),
			"Days":         days,
			"Period":       reportPeriodParam(r),
			"Anchor":       window.Anchor,
			"PeriodLabel":  window.Label,
			"YoYHas":       lastYear.Count > 0,
			"YoYNow":       curPeriod.Total,
			"YoYThen":      lastYear.Total,
			"YoYPct":       yoyPct,
			"RunningOut":   runningOut,
			"GrandTotal":   grandTotal,
			"GrandTax":     grandTax,
			"GrandCount":   grandCount,
			"GrandRefunds": grandRefunds,
			"GrandNet":     grandNet,
		})(w, r)
	})

	// On-demand report tabs: each runs its queries only when the operator
	// actually opens the tab (htmx click → fragment swap, ADR-0008 pattern;
	// the /ui/ namespace is denylisted from manual route coverage — the
	// enclosing /reports topic covers these fragments). Reads the exact
	// same ?period/?anchor/?days params as /reports via parseReportWindow
	// (ut-docs#519), so a tab shows the same window as the header KPIs.
	mux.HandleFunc("/ui/reports/tab/{name}", func(w http.ResponseWriter, r *http.Request) {
		bizDayStart, _, _ := d.Settings.Get(r.Context(), keyReportsBusinessDayStart)
		window := parseReportWindow(r, bizDayStart)
		repo := data.NewPOSRepo(d.Db)
		switch r.PathValue("name") {
		case "sales-trend":
			daily, _ := repo.SalesByDay(r.Context(), window.From, window.To, window.Hour, window.Minute)
			byWeekday, _ := repo.SalesByWeekday(r.Context(), window.From, window.To, window.Hour, window.Minute)
			byHour, _ := repo.SalesByHour(r.Context(), window.From, window.To, window.Hour, window.Minute)
			// Normalize to bar widths (busiest = 100%) so the template stays dumb.
			type busyBar struct {
				Label string
				Count int
				Total int64
				Pct   int
			}
			weekdayKeys := []string{"reports.wd_sun", "reports.wd_mon", "reports.wd_tue", "reports.wd_wed", "reports.wd_thu", "reports.wd_fri", "reports.wd_sat"}
			maxCount := 0
			for _, b := range byWeekday {
				if b.Count > maxCount {
					maxCount = b.Count
				}
			}
			weekdayBars := make([]busyBar, 0, len(byWeekday))
			for _, b := range byWeekday {
				if b.Slot < 0 || b.Slot > 6 {
					continue
				}
				bar := busyBar{Label: weekdayKeys[b.Slot], Count: b.Count, Total: b.Total}
				if maxCount > 0 {
					bar.Pct = b.Count * 100 / maxCount
				}
				weekdayBars = append(weekdayBars, bar)
			}
			maxCount = 0
			for _, b := range byHour {
				if b.Count > maxCount {
					maxCount = b.Count
				}
			}
			hourBars := make([]busyBar, 0, len(byHour))
			for _, b := range byHour {
				bar := busyBar{Label: fmt.Sprintf("%02d:00", b.Slot), Count: b.Count, Total: b.Total}
				if maxCount > 0 {
					bar.Pct = b.Count * 100 / maxCount
				}
				hourBars = append(hourBars, bar)
			}
			httpx.RenderPartial("ui/partials/reports_tab_sales_trend.html", map[string]any{
				"Daily":       daily,
				"WeekdayBars": weekdayBars,
				"HourBars":    hourBars,
			})(w, r)
		case "items":
			top, _ := repo.TopItems(r.Context(), window.From, window.To, 10)
			slow, _ := repo.SlowItems(r.Context(), window.From, window.To, 10)
			dead, _ := repo.DeadStock(r.Context(), window.From, window.To, 10)
			margins, _ := repo.MarginByItem(r.Context(), window.From, window.To, 10)
			httpx.RenderPartial("ui/partials/reports_tab_items.html", map[string]any{
				"Top":       top,
				"Slow":      slow,
				"DeadStock": dead,
				"Margins":   margins,
			})(w, r)
		case "tax":
			taxBands, _ := repo.TaxSummary(r.Context(), window.From, window.To)
			type taxRow struct {
				Rate string
				Net  int64
				Tax  int64
			}
			taxRows := make([]taxRow, 0, len(taxBands))
			for _, b := range taxBands {
				taxRows = append(taxRows, taxRow{Rate: fmt.Sprintf("%.4g%%", float64(b.RateBP)/100), Net: b.Net, Tax: b.Tax})
			}
			httpx.RenderPartial("ui/partials/reports_tab_tax.html", map[string]any{
				"TaxRows": taxRows,
			})(w, r)
		case "forecast":
			seasonal, seasonalCats, _ := repo.SeasonalForecast(r.Context(), 28, 10)
			// The rollup only earns its screen space with real categories — a
			// lone "uncategorized" bucket restates the item table.
			hasNamedCat := false
			for _, c := range seasonalCats {
				if c.Name != "" {
					hasNamedCat = true
					break
				}
			}
			if !hasNamedCat {
				seasonalCats = nil
			}
			httpx.RenderPartial("ui/partials/reports_tab_forecast.html", map[string]any{
				"Seasonal":     seasonal,
				"SeasonalCats": seasonalCats,
			})(w, r)
		case "payments":
			methods, _ := repo.PaymentBreakdown(r.Context(), window.From, window.To)
			departments, _ := repo.SalesByDepartment(r.Context(), window.From, window.To)
			// Per-till breakdown is only meaningful once a shop runs more than one
			// register (department stores) — hide it for single-till shops.
			tills, _ := repo.SalesByTill(r.Context(), window.From, window.To)
			if len(tills) < 2 {
				tills = nil
			}
			// Manual cash adjustments/payouts (float top-ups, till-count
			// corrections, Pfandrückgabe payouts) grouped by reason —
			// ut-docs#267: SumShiftAdjustments only ever gives one net
			// total per shift, with no way to pull e.g. "total
			// Pfandrückgabe paid out this period". Gated on the "audit"
			// action, same as it is today: this data only otherwise
			// surfaces via /audit (audit_page.go), which is manager/admin
			// only ("this reads system-wide history") — reasons are staff
			// free text (e.g. "cash short - Anna's till"), so a reporting
			// shortcut must not widen who can read it beyond that gate.
			var cashAdjustments []data.CashAdjustmentReasonTotal
			if canPerform(d, r, "audit") {
				cashAdjustments, _ = repo.CashAdjustmentsByReason(r.Context(), window.From, window.To)
			}
			httpx.RenderPartial("ui/partials/reports_tab_payments.html", map[string]any{
				"Methods":         methods,
				"Departments":     departments,
				"Tills":           tills,
				"CashAdjustments": cashAdjustments,
			})(w, r)
		case "eod":
			// ut-docs#794 review finding (blocker): this used to gate the
			// WHOLE tab — buttons included — on eod_report, the exact same
			// action checkOrElevate now gates each POST handler on. Once a
			// shop customizes role_permissions so a role holds `reports`
			// (can view the Reports page at all) but not `eod_report`
			// (can run/approve EOD), that role must see the buttons to
			// EVER trigger the elevation dialog — gating visibility on
			// eod_report made the dialog dead code for exactly the
			// cashier-with-manager-approval scenario ADR-0052 exists for.
			// Mirrors the settings.html/backup_api.go precedent: the page-
			// level view permission (`reports`, matching this tab's own
			// outer /reports page gate) controls whether the card renders
			// at all; checkOrElevate("eod_report") is the real
			// authorization boundary on each action.
			canView := canPerform(d, r, "reports")
			canRunEOD := canPerform(d, r, "eod_report")
			type eodRow struct {
				Period string
				Net    int64
				Sales  int
				// HasVariance flags a non-zero cash-count variance in the
				// archived report's cash reconciliation (ut-docs#1006), so
				// a discrepancy is visible on screen without reprinting
				// every period. Gated behind CanRunEOD with Net/Sales —
				// it's derived from the same report history.
				HasVariance bool
			}
			var eodRows []eodRow
			// ut-docs#794 review finding (residual on the blocker-1 fix):
			// the row list itself — just the periods, so the Reprint
			// button has something to attach to — is shown to anyone who
			// can view the tab at all, same as the schedule below,
			// otherwise print/{period}'s elevation dialog stays exactly as
			// unreachable as the rest of the tab was before that fix (its
			// only trigger lives in this table). The money figures
			// (Net/Sales) stay behind eod_report specifically — real
			// report history, gated same as before — so they're populated
			// only when canRunEOD; the template renders those two columns
			// only when CanRunEOD is set. Still skipped entirely for a
			// non-viewer (the original perf rationale — no repo call for a
			// role that gets no card at all).
			if canView {
				archived, _ := repo.ListArchivedReports(r.Context(), 14)
				for _, a := range archived {
					if a.Kind != "eod" {
						continue
					}
					row := eodRow{Period: a.Period}
					if canRunEOD {
						var rep data.EODReport
						if json.Unmarshal([]byte(a.Content), &rep) == nil {
							row.Net = rep.Net
							row.Sales = rep.SalesCount
							row.HasVariance = rep.CashReconciliation != nil && rep.CashReconciliation.Variance != 0
						}
					}
					eodRows = append(eodRows, row)
				}
			}
			// The schedule (enabled/time) is operational, not financial —
			// shown to anyone who can view the tab at all, same as
			// BusinessDayStart already is, so the settings form reflects
			// real state for an operator who may need a manager's approval
			// to change it.
			var eodEnabled, eodTime string
			if canView {
				eodEnabled, _, _ = d.Settings.Get(r.Context(), keyEODEnabled)
				eodTime, _, _ = d.Settings.Get(r.Context(), keyEODTime)
			}
			httpx.RenderPartial("ui/partials/reports_tab_eod.html", map[string]any{
				"IsManager":        canView,
				"CanRunEOD":        canRunEOD,
				"EODRows":          eodRows,
				"EODEnabled":       eodEnabled == "true",
				"EODTime":          eodTime,
				"BusinessDayStart": bizDayStart,
			})(w, r)
		case "tips":
			renderTipsTab(repo, d, r, window)(w, r)
		default:
			http.NotFound(w, r)
		}
	})

	registerWorkerAllocationAPI(mux, d)
}

// workerAllocationDisplayNames maps every user's cashier_id to a
// human-readable name (DisplayName, falling back to Username) via the same
// AuthRepo.ListUsers lookup the tab's worker picker and cashier filter use
// — falls back to the raw id when the user row can't be found (e.g. a
// since-deleted account), per #964's brief: "don't just dump the raw id if
// a display name is available".
func workerAllocationDisplayNames(users []data.UserRow) map[string]string {
	names := make(map[string]string, len(users))
	for _, u := range users {
		name := u.DisplayName
		if name == "" {
			name = u.Username
		}
		names[u.ID] = name
	}
	return names
}

// workerAllocationDateRange converts a reportWindow's [From, To) instant
// range into the inclusive "YYYY-MM-DD" date-string pair
// WorkerAllocationsSummary/ListWorkerAllocations expect (their own
// date(allocated_at, 'localtime') BETWEEN date(?) AND date(?) convention,
// ut-docs#869 — see WorkerAllocationsSummary's doc comment) — window.To is
// EXCLUSIVE (parseReportWindow's own convention, matching SalesByDay et al),
// so it's stepped back a second before mapping to a calendar date, the same
// one-second buffer reportNow() itself adds on the other end, to avoid
// inclusively pulling in the following calendar day.
//
// Both ends are mapped through businessDateFor(t, window.Hour, window.Minute)
// — the SAME business-day-start boundary the window itself was built from
// (parseReportWindow) — rather than formatted directly (ut-docs#1020 item
// 6). A raw .Format("2006-01-02") is only correct when the business day
// starts at midnight: with e.g. a 06:00 start, a single ?period=day report
// resolves to the half-open instant range [day 06:00, day+1 06:00), and
// formatting window.To.Add(-time.Second) directly yields "day+1", not
// "day" — so a one-day report's own date range spanned TWO calendar days,
// and a ?period=month report spilled one day into the next month. Every
// payout recorded on that spillover day was then double-counted: present
// in both the report it actually belongs to and the following one.
// businessDateFor resolves an instant to the calendar date its BUSINESS
// day belongs to (an instant before hh:mm belongs to the previous calendar
// date), which is exactly what both From and the (already-decremented)
// To need mapped through to land back on the single calendar date the
// window was actually built to represent.
func workerAllocationDateRange(window reportWindow) (from, to string) {
	from = businessDateFor(window.From, window.Hour, window.Minute).Format("2006-01-02")
	to = businessDateFor(window.To.Add(-time.Second), window.Hour, window.Minute).Format("2006-01-02")
	return from, to
}

// workerAllocationRequestedAt validates a manager-picked "YYYY-MM-DD" date
// against nowLocal's LOCAL calendar day and, if it isn't in the future,
// builds the UTC instant to store as allocated_at. Pulled out as its own
// pure function (independent review, ut-docs#964 blocker) so the future-day
// check and the stored instant are computed from the exact same nowLocal —
// they cannot disagree with each other the way the original inline version
// could when it mixed a UTC "today" comparison with a since-corrected local
// construction, and so this is unit-testable without depending on either
// the host's real TZ or the wall clock at test time.
//
// nowLocal MUST be in the shop's local location (callers pass time.Now(),
// which already is) — every other clock this tab touches (parseReportWindow,
// reportNow, generateEOD's own day boundary, and the read side's own
// date(allocated_at, 'localtime')) is local, not UTC. A UTC "today" here
// would reject a real today as "in the future" for the last 1-3 hours of
// every trading day in Turkey (UTC+3) or UK BST (UTC+1), and would silently
// accept a real tomorrow as valid for most of the day in any Americas shop.
//
// The stored instant is built by taking nowLocal's own wall-clock
// time-of-day and swapping in the picked calendar date, in the SAME
// location, before converting to UTC — so date(allocatedAt, 'localtime')
// on the read side always resolves back to exactly the date the manager
// picked, never a neighbouring calendar day.
func workerAllocationRequestedAt(date string, nowLocal time.Time) (allocatedAt string, isFuture bool, err error) {
	loc := nowLocal.Location()
	pickedDate, err := time.ParseInLocation("2006-01-02", date, loc)
	if err != nil {
		return "", false, err
	}
	if date > nowLocal.Format("2006-01-02") {
		return "", true, nil
	}
	at := time.Date(pickedDate.Year(), pickedDate.Month(), pickedDate.Day(),
		nowLocal.Hour(), nowLocal.Minute(), nowLocal.Second(), 0, loc)
	return at.UTC().Format(time.RFC3339), false, nil
}

// tipsDetailRow is one combined-source-type row for the "tips" tab's detail
// table — ADR-0063's own worker_allocations rows, joined here (not in SQL)
// to a human worker name, since ListWorkerAllocations is deliberately
// single-source_type (see its own doc comment).
type tipsDetailRow struct {
	AllocatedAt string
	Worker      string
	SourceType  string
	AmountMinor int64
	Note        string
}

// renderTipsTab builds and renders the "tips" tab fragment — factored out
// of the tab switch so the record-payout POST handler can re-render the
// SAME fragment after a successful write (the htmx swap target sees updated
// totals immediately), rather than duplicating this view-building logic.
//
// Visibility mirrors the "eod" tab's own CanView/CanRunEOD split (ut-docs#794):
// CanView (`reports`) gates whether ANYTHING here is populated at all — the
// two received-vs-allocated summary totals are visible under CanView alone.
// CanRecord (`worker_allocation`) additionally gates the row-level detail
// table and the record-a-payout form/export link, since those expose (or
// let someone write) individual workers' payout records, not just an
// aggregate total.
func renderTipsTab(repo *data.POSRepo, d *common.Deps, r *http.Request, window reportWindow) http.HandlerFunc {
	ctx := r.Context()
	canView := canPerform(d, r, "reports")
	canRecord := canPerform(d, r, "worker_allocation")
	// The ?cashier= filter is only honored for a canRecord session
	// (independent review, ut-docs#964 blocker: a canView-only session —
	// holding `reports` but not `worker_allocation` — could otherwise
	// read ANY named worker's received/allocated totals by picking them
	// from the worker dropdown the tab itself renders; the doc comment
	// below already claimed per-worker detail needs `worker_allocation`,
	// this makes the per-worker SUMMARY actually honor that too, not just
	// the row-level table). A canView-only session always sees the
	// shop-wide total regardless of what's in the query string.
	cashierFilter := ""
	if canRecord {
		cashierFilter = strings.TrimSpace(r.URL.Query().Get("cashier"))
	}

	var tipSummary, scSummary data.WorkerAllocationSummary
	var workers []data.UserRow
	var detail []tipsDetailRow
	from, to := workerAllocationDateRange(window)

	if canView {
		tipSummary, _ = repo.WorkerAllocationsSummary(ctx, from, to, cashierFilter, "tip")
		scSummary, _ = repo.WorkerAllocationsSummary(ctx, from, to, cashierFilter, "service_charge")
		if canRecord {
			allUsers, _ := data.NewAuthRepo(d.Db).ListUsers(ctx)
			workers = allUsers
			names := workerAllocationDisplayNames(allUsers)
			tipRows, _ := repo.ListWorkerAllocations(ctx, from, to, cashierFilter, "tip")
			scRows, _ := repo.ListWorkerAllocations(ctx, from, to, cashierFilter, "service_charge")
			merged := make([]data.WorkerAllocation, 0, len(tipRows)+len(scRows))
			merged = append(merged, tipRows...)
			merged = append(merged, scRows...)
			sort.Slice(merged, func(i, j int) bool { return merged[i].AllocatedAt > merged[j].AllocatedAt })
			for _, m := range merged {
				worker := names[m.CashierID]
				if worker == "" {
					worker = m.CashierID
				}
				detail = append(detail, tipsDetailRow{
					AllocatedAt: m.AllocatedAt,
					Worker:      worker,
					SourceType:  m.SourceType,
					AmountMinor: m.AmountMinor,
					Note:        m.Note,
				})
			}
		}
	}

	return httpx.RenderPartial("ui/partials/reports_tab_tips.html", map[string]any{
		"StoreName":     storeNameOrDefault(ctx, d),
		"CanView":       canView,
		"CanRecord":     canRecord,
		"CashierFilter": cashierFilter,
		"Workers":       workers,
		"TipReceived":   tipSummary.ReceivedMinor,
		"TipAllocated":  tipSummary.AllocatedMinor,
		"SCReceived":    scSummary.ReceivedMinor,
		"SCAllocated":   scSummary.AllocatedMinor,
		"Detail":        detail,
		"From":          from,
		"To":            to,
		// LOCAL, matching the POST handler's own future-date check (fixed
		// alongside this, ut-docs#964 review) — both must agree, and local
		// is correct: parseReportWindow/reportNow/generateEOD/the read
		// side's date(...,'localtime') are all local already, so a UTC
		// "today" here was the one clock actually out of step, not the
		// other way around as the previous comment claimed.
		"Today":  time.Now().Format("2006-01-02"),
		"Days":   parseReportDays(r),
		"Period": reportPeriodParam(r),
		"Anchor": window.Anchor,
	})
}

// registerWorkerAllocationAPI mounts the record-a-payout POST and the CSV
// export GET behind the "tips" tab (ut-docs#964). A separate function
// (rather than inlining the two mux.HandleFunc calls directly in the
// /ui/reports/tab/{name} closure) purely to keep registerReportsPage's
// switch body from growing two more unrelated route registrations inside
// it — called once from registerReportsPage itself, no new call site needed
// in init.go.
func registerWorkerAllocationAPI(mux *http.ServeMux, d *common.Deps) {
	repo := data.NewPOSRepo(d.Db)
	authRepo := data.NewAuthRepo(d.Db)

	mux.HandleFunc("POST /api/reports/worker-allocations", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "worker_allocation") {
			http.Error(w, "worker_allocation permission required", http.StatusForbidden)
			return
		}
		ctx := r.Context()
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form data", http.StatusBadRequest)
			return
		}

		date := strings.TrimSpace(r.FormValue("date"))
		if !eodDateRe.MatchString(date) {
			http.Error(w, "date must be YYYY-MM-DD", http.StatusBadRequest)
			return
		}

		cashierID := strings.TrimSpace(r.FormValue("cashier_id"))
		if cashierID == "" {
			http.Error(w, "cashier_id is required", http.StatusBadRequest)
			return
		}
		if _, found, err := authRepo.GetUser(ctx, cashierID); err != nil || !found {
			http.Error(w, "cashier_id must be a real user id", http.StatusBadRequest)
			return
		}

		sourceType := strings.TrimSpace(r.FormValue("source_type"))
		if sourceType != "tip" && sourceType != "service_charge" {
			http.Error(w, `source_type must be "tip" or "service_charge"`, http.StatusBadRequest)
			return
		}

		amtStr := strings.TrimSpace(r.FormValue("amount"))
		amountMinor, err := strconv.ParseInt(amtStr, 10, 64)
		if err != nil || amountMinor <= 0 {
			http.Error(w, "amount must be a positive integer (minor units)", http.StatusBadRequest)
			return
		}

		note := r.FormValue("note")

		allocatedAt, isFuture, err := workerAllocationRequestedAt(date, time.Now())
		if err != nil {
			http.Error(w, "date must be YYYY-MM-DD", http.StatusBadRequest)
			return
		}
		if isFuture {
			http.Error(w, "date must not be in the future", http.StatusBadRequest)
			return
		}
		id := uuid.NewString()

		tx, err := d.Db.BeginTx(ctx, nil)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "reports.tips.error.save", "worker_allocation_record", err)
			return
		}
		defer tx.Rollback()

		if err := repo.InsertWorkerAllocation(ctx, tx, id, sourceType, "", cashierID, amountMinor, allocatedAt, note); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "reports.tips.error.save", "worker_allocation_record", err)
			return
		}
		actorID := getSessionUserID(r)
		now := time.Now().UTC().Format(time.RFC3339)
		if err := repo.InsertAudit(ctx, tx, actorID, "worker_allocation", id, "worker_allocation_recorded",
			map[string]any{"source_type": sourceType, "cashier_id": cashierID, "amount_minor": amountMinor, "note": note}, now, ""); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "reports.tips.error.save", "worker_allocation_record", err)
			return
		}
		if err := tx.Commit(); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "reports.tips.error.save", "worker_allocation_record", err)
			return
		}

		// Re-render the same "tips" tab fragment the GET route serves, so
		// the htmx swap shows the just-recorded payout's updated totals in
		// place — same window params (?days/period/anchor) this POST's own
		// form re-submits (reports_tab_tips.html wires them as hidden
		// fields on the record form for exactly this). parseReportWindow/
		// renderTipsTab read r.URL.Query() (the GET-tab route's own
		// convention), which is empty for this POST's body-encoded fields —
		// so those already-parsed form values are copied onto r.URL.RawQuery
		// here, after every r.FormValue() read above, so the re-render sees
		// the SAME window (and cashier filter) the operator had open rather
		// than silently resetting to the 14-day default.
		q := url.Values{}
		if period := r.FormValue("period"); period != "" {
			q.Set("period", period)
			q.Set("anchor", r.FormValue("anchor"))
		} else if days := r.FormValue("days"); days != "" {
			q.Set("days", days)
		}
		if cashier := r.FormValue("cashier"); cashier != "" {
			q.Set("cashier", cashier)
		}
		r.URL.RawQuery = q.Encode()

		bizDayStart, _, _ := d.Settings.Get(ctx, keyReportsBusinessDayStart)
		window := parseReportWindow(r, bizDayStart)
		renderTipsTab(repo, d, r, window)(w, r)
	})

	mux.HandleFunc("GET /api/reports/worker-allocations/export", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "worker_allocation") {
			http.Error(w, "worker_allocation permission required", http.StatusForbidden)
			return
		}
		ctx := r.Context()
		from := strings.TrimSpace(r.URL.Query().Get("from"))
		to := strings.TrimSpace(r.URL.Query().Get("to"))
		if !eodDateRe.MatchString(from) || !eodDateRe.MatchString(to) {
			http.Error(w, "from and to must be YYYY-MM-DD", http.StatusBadRequest)
			return
		}
		if from > to {
			http.Error(w, "from must not be after to", http.StatusBadRequest)
			return
		}
		cashierID := strings.TrimSpace(r.URL.Query().Get("cashier"))

		// Same tolerant "" -> raw id fallback as the tab's own worker-name
		// lookup (renderTipsTab) — a lookup failure degrades to raw ids
		// rather than blocking the export outright, matching this
		// codebase's existing ListUsers-error convention elsewhere
		// (audit_page.go, users_page.go: `_, _ := ...ListUsers(...)`).
		allUsers, _ := authRepo.ListUsers(ctx)
		names := workerAllocationDisplayNames(allUsers)

		tipRows, err := repo.ListWorkerAllocations(ctx, from, to, cashierID, "tip")
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "reports.tips.error.export", "worker_allocation_export", err)
			return
		}
		scRows, err := repo.ListWorkerAllocations(ctx, from, to, cashierID, "service_charge")
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "reports.tips.error.export", "worker_allocation_export", err)
			return
		}
		rows := make([]data.WorkerAllocation, 0, len(tipRows)+len(scRows))
		rows = append(rows, tipRows...)
		rows = append(rows, scRows...)
		sort.Slice(rows, func(i, j int) bool { return rows[i].AllocatedAt > rows[j].AllocatedAt })

		now := time.Now().UTC().Format(time.RFC3339)
		actorID := getSessionUserID(r)
		_ = repo.InsertAudit(ctx, nil, actorID, "worker_allocation", "-", "worker_allocation_exported",
			map[string]any{"from": from, "to": to, "cashier": cashierID}, now, "")

		filename := fmt.Sprintf("worker-allocations-%s-to-%s.csv", from, to)
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
		w.WriteHeader(http.StatusOK)
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"date", "worker", "source_type", "amount_minor", "note"})
		for _, row := range rows {
			worker := names[row.CashierID]
			if worker == "" {
				worker = row.CashierID
			}
			// csvSafe on worker + note (ut-docs#1020 item 2): note is a
			// manager-typed free-text field, and worker falls back to a
			// user's own DisplayName/Username — both operator-set text
			// that opens in Excel/Sheets unescaped, where a field
			// starting with =/+/-/@ becomes a live formula. This export's
			// own help text frames it as for "a worker, an accountant, or
			// anyone else" to open directly.
			_ = cw.Write([]string{row.AllocatedAt, csvSafe(worker), row.SourceType, strconv.FormatInt(row.AmountMinor, 10), csvSafe(row.Note)})
		}
		cw.Flush()
		if err := cw.Error(); err != nil {
			// Headers and a 200 are already on the wire (same precedent as
			// eod_api.go's archive/export) -- log rather than panic.
			logging.L().Errorf("worker allocation export: csv write: %v", err)
		}
	})
}
