package pages

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
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
			// Pfandrückgabe paid out this period".
			cashAdjustments, _ := repo.CashAdjustmentsByReason(r.Context(), window.From, window.To)
			httpx.RenderPartial("ui/partials/reports_tab_payments.html", map[string]any{
				"Methods":         methods,
				"Departments":     departments,
				"Tills":           tills,
				"CashAdjustments": cashAdjustments,
			})(w, r)
		case "eod":
			// Gated by the eod_report action (#709/#555) before the repo calls,
			// not just in the template: the partial only ever renders its body
			// {{ if .IsManager }}, so a non-manager gets nothing back either
			// way — but pre-this-fix, ListArchivedReports and two Settings.Get
			// calls still ran for a role that can never see the result.
			isManager := canPerform(d, r, "eod_report")
			type eodRow struct {
				Period string
				Net    int64
				Sales  int
			}
			var eodRows []eodRow
			var eodEnabled, eodTime string
			if isManager {
				archived, _ := repo.ListArchivedReports(r.Context(), 14)
				for _, a := range archived {
					if a.Kind != "eod" {
						continue
					}
					var rep data.EODReport
					if json.Unmarshal([]byte(a.Content), &rep) == nil {
						eodRows = append(eodRows, eodRow{Period: a.Period, Net: rep.Net, Sales: rep.SalesCount})
					}
				}
				eodEnabled, _, _ = d.Settings.Get(r.Context(), keyEODEnabled)
				eodTime, _, _ = d.Settings.Get(r.Context(), keyEODTime)
			}
			httpx.RenderPartial("ui/partials/reports_tab_eod.html", map[string]any{
				"IsManager":        isManager,
				"EODRows":          eodRows,
				"EODEnabled":       eodEnabled == "true",
				"EODTime":          eodTime,
				"BusinessDayStart": bizDayStart,
			})(w, r)
		default:
			http.NotFound(w, r)
		}
	})
}
