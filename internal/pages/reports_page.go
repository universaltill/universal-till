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
		rates, ratesErr := repo.ItemDirectDailySellRates(r.Context(), sellRateNow.Add(-28*24*time.Hour), sellRateNow)
		variantRates, variantRatesErr := repo.VariantDailySellRates(r.Context(), sellRateNow.Add(-28*24*time.Hour), sellRateNow)
		if ratesErr == nil && variantRatesErr == nil && (len(rates) > 0 || len(variantRates) > 0) {
			if lvls, err := repo.ListStockLevels(r.Context()); err == nil {
				for _, l := range lvls {
					if l.IsRunningOut(l.SellRate(rates, variantRates)) {
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
		grandDiscount, _ := repo.DiscountsByWindow(r.Context(), window.From, window.To)
		grandNet := grandTotal - grandRefunds
		// Avg sale (ut-docs#1974, SumUp Insights parity): a derived display
		// value from the two grand totals already computed above, not a new
		// query. Zero-sale-safe — an empty window must render £0.00, not
		// divide by zero.
		var grandAvg int64
		if grandCount > 0 {
			grandAvg = grandTotal / int64(grandCount)
		}

		httpx.Render("ui/pages/reports.html", map[string]any{
			"title":     httpx.T(httpx.RequestLocale(r), "page.title.reports"),
			"theme":     d.CurrentState().Theme,
			"menuItems": d.MenuSnapshot(),
			"CanAsk":    aiService(r.Context(), d).CanAsk() && canPerform(d, r, "reports"),
			"IsManager": canPerform(d, r, "reports"),
			// ut-docs#1465: gates the "Shrinkage & Loss" tab button's own
			// visibility on void_comp_waste specifically (not the generic
			// "reports" IsManager flag above) -- same "eod" tab model this
			// file's own /ui/reports/tab/{name} switch documents.
			"CanViewShrinkage": canPerform(d, r, "void_comp_waste"),
			// ut-docs#988: the "yüzde usulü" pool tab is a Turkey-only
			// obligation (İş Kanunu 4857 art. 51) with no meaning anywhere
			// else, so its tab button is gated on the shop's configured
			// country rather than on a permission — the permission gate
			// inside the tab is still `worker_allocation`, shared with the
			// Tips tab (ADR-0063: one ledger, two filtered views).
			"ShowYuzdeUsulu": d.CurrentState().Country == "TR",
			"Days":           days,
			"Period":         reportPeriodParam(r),
			"Anchor":         window.Anchor,
			"PeriodLabel":    window.Label,
			"YoYHas":         lastYear.Count > 0,
			"YoYNow":         curPeriod.Total,
			"YoYThen":        lastYear.Total,
			"YoYPct":         yoyPct,
			"RunningOut":     runningOut,
			"GrandTotal":     grandTotal,
			"GrandTax":       grandTax,
			"GrandDiscount":  grandDiscount,
			"GrandCount":     grandCount,
			"GrandAvg":       grandAvg,
			"GrandRefunds":   grandRefunds,
			"GrandNet":       grandNet,
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
			taxBands, _ := computeTaxSummary(r.Context(), repo, window.From, window.To)
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
		case "shrinkage":
			// ut-docs#1465 (G41): "Shrinkage & Loss" -- gated on the SAME
			// void_comp_waste permission /api/pos/remove's reason gate
			// itself checks (not the generic "reports" IsManager flag,
			// unlike the "eod" tab's own two-permission split below): this
			// tab has no separate elevation-gated action of its own to
			// split view-vs-act on, so one permission does both jobs. The
			// button that opens this tab is hidden from a role that can't
			// view it (reports.html's CanViewShrinkage), but the handler
			// re-checks directly too -- defense in depth against a direct
			// GET, same reasoning as every other canPerform-gated tab.
			var byReason []data.ShrinkageReasonTotal
			var topItems []data.TopItem
			if canPerform(d, r, "void_comp_waste") {
				byReason, _ = repo.ShrinkageByReason(r.Context(), window.From, window.To)
				topItems, _ = repo.ShrinkageTopItems(r.Context(), window.From, window.To, 10)
			}
			var totalValue int64
			var totalCount int
			for _, rt := range byReason {
				totalValue += rt.Total
				totalCount += rt.Count
			}
			httpx.RenderPartial("ui/partials/reports_tab_shrinkage.html", map[string]any{
				"ByReason":   byReason,
				"TopItems":   topItems,
				"TotalValue": totalValue,
				"TotalCount": totalCount,
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
				// Tips (ut-docs#1529) is the same total already broken out
				// on the PRINTED Z-report's "TIPS (held out of revenue)"
				// footer and the JSON export (rep.Tips, ut-docs#1007) — a
				// manager glancing at this tab without printing/downloading
				// previously had no visibility into it at all. Gated behind
				// CanRunEOD with Net/Sales, same reasoning: it's a real
				// money figure from the same report history.
				Tips int64
				// From/To (ADR-0066 Decision 6, ut-docs#1141): set only
				// for a close-to-close "eod" report (rep.Day == "", the
				// new path) — the template renders "From – To" in place
				// of the bare Period column for these rows, matching the
				// reference document's own Zeitraum line. Empty for a
				// legacy calendar-date row, which keeps showing its
				// plain Period as before (no regression on the
				// historical path).
				From, To string
				// ArticleGroups/Articles/Operators (ut-docs#1010) come from
				// the same archived report's content_json the Net/Sales
				// fields above already unmarshal from — gated behind
				// CanRunEOD the same way, and left nil (no section
				// rendered, see the template) for any report these weren't
				// computed for (a range report, or one archived before
				// this change).
				ArticleGroups []data.ArticleGroupSales
				Articles      []data.ArticleSales
				Operators     []data.OperatorSales
				// OrderTypes (ut-docs#1015) is the same archived-report,
				// same-gate, same-nil-when-absent breakdown as the three
				// above.
				OrderTypes []data.OrderTypeSales
				// FiscalDevice (ut-docs#2410) is the same archived-report,
				// same-gate, same-nil-when-absent breakdown as the
				// ArticleGroups/OrderTypes fields above — nil (no section
				// rendered) for every non-TR shop or a report archived
				// before this card.
				FiscalDevice *data.FiscalDeviceWindow
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
							for _, tp := range rep.Tips {
								row.Tips += tp.Amount
							}
							row.ArticleGroups = rep.ArticleGroups
							row.Articles = rep.Articles
							row.Operators = rep.Operators
							row.OrderTypes = rep.OrderTypes
							row.FiscalDevice = rep.FiscalDevice
							if rep.Day == "" {
								row.From, row.To = rep.From, rep.To
							}
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
			var articlePrintMode string
			var articlePrintCap int
			if canView {
				eodEnabled, _, _ = d.Settings.Get(r.Context(), keyEODEnabled)
				eodTime, _, _ = d.Settings.Get(r.Context(), keyEODTime)
				// ut-docs#1650: reflects the RESOLVED (defaulted) state, same
				// as EODEnabled/EODTime/BusinessDayStart above, so a store
				// that never touched this setting sees the shipped default
				// ("capped"/30) pre-selected rather than a blank/unset form.
				articlePrintMode, articlePrintCap = resolveEODArticlePrintSettings(r.Context(), d)
			}
			httpx.RenderPartial("ui/partials/reports_tab_eod.html", map[string]any{
				"IsManager":        canView,
				"CanRunEOD":        canRunEOD,
				"EODRows":          eodRows,
				"EODEnabled":       eodEnabled == "true",
				"EODTime":          eodTime,
				"BusinessDayStart": bizDayStart,
				"ArticlePrintMode": articlePrintMode,
				"ArticlePrintCap":  articlePrintCap,
			})(w, r)
		case "tips":
			renderTipsTab(repo, d, r, window)(w, r)
		case "yuzde-usulu":
			renderYuzdeUsuluTab(repo, d, r, window)(w, r)
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

// yuzdeUsuluCollectionRow is one yuzde_usulu_pool_collections row for the
// "yuzde-usulu" tab's collections table and its pool picker — the same
// join-in-Go-not-SQL shape as tipsDetailRow above, resolving recorded_by to
// a human name through workerAllocationDisplayNames rather than dumping a
// raw user id.
type yuzdeUsuluCollectionRow struct {
	ID          string
	CollectedAt string
	AmountMinor int64
	BasisNote   string
	RecordedBy  string
}

// renderYuzdeUsuluTab builds and renders the "yuzde-usulu" tab fragment —
// Turkey's İş Kanunu 4857 art. 51 "yüzde usulü" pool view (ut-docs#988,
// ADR-0063's own step 2/2). Factored out of the tab switch for the same
// reason renderTipsTab is: both POST handlers behind this tab re-render
// this SAME fragment on success, so the htmx swap shows updated totals in
// place.
//
// Structurally this mirrors renderTipsTab deliberately — same CanView
// (`reports`) / CanRecord (`worker_allocation`) split, same reused
// permission (ADR-0063 Decision 2: one ledger, two filtered views, so a
// second permission would only let the two obligations drift apart), and
// the same rule that a ?cashier= filter is honored ONLY for a CanRecord
// session (ut-docs#964's own review blocker: otherwise a `reports`-only
// session could read any named worker's totals straight off the query
// string).
//
// The two KPIs are the point of this tab and of this card: Collected reads
// the independent yuzde_usulu_pool_collections record, Distributed reads
// worker_allocations. Before ut-docs#988 those were the same rows summed
// twice (see WorkerAllocationsSummary's doc comment), so the comparison
// could not say anything; they can now legitimately differ.
func renderYuzdeUsuluTab(repo *data.POSRepo, d *common.Deps, r *http.Request, window reportWindow) http.HandlerFunc {
	ctx := r.Context()
	canView := canPerform(d, r, "reports")
	canRecord := canPerform(d, r, "worker_allocation")
	cashierFilter := ""
	if canRecord {
		cashierFilter = strings.TrimSpace(r.URL.Query().Get("cashier"))
	}

	var summary data.WorkerAllocationSummary
	var workers []data.UserRow
	var collections []yuzdeUsuluCollectionRow
	var detail []tipsDetailRow
	from, to := workerAllocationDateRange(window)

	if canView {
		// One query pair for both KPIs: summary.ReceivedMinor IS the
		// collections total (that source_type's branch calls
		// YuzdeUsuluPoolCollectionsTotal), deliberately unscoped by
		// cashier — a pool's collected side is the whole pool, never one
		// worker's share — while AllocatedMinor honors cashierFilter.
		summary, _ = repo.WorkerAllocationsSummary(ctx, from, to, cashierFilter, "yuzde_usulu_pool")
		if canRecord {
			allUsers, _ := data.NewAuthRepo(d.Db).ListUsers(ctx)
			workers = allUsers
			names := workerAllocationDisplayNames(allUsers)

			poolRows, _ := repo.ListYuzdeUsuluPoolCollections(ctx, from, to)
			for _, p := range poolRows {
				recordedBy := names[p.RecordedBy]
				if recordedBy == "" {
					recordedBy = p.RecordedBy
				}
				collections = append(collections, yuzdeUsuluCollectionRow{
					ID:          p.ID,
					CollectedAt: p.CollectedAt,
					AmountMinor: p.AmountMinor,
					BasisNote:   p.BasisNote,
					RecordedBy:  recordedBy,
				})
			}

			allocRows, _ := repo.ListWorkerAllocations(ctx, from, to, cashierFilter, "yuzde_usulu_pool")
			for _, m := range allocRows {
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

	return httpx.RenderPartial("ui/partials/reports_tab_yuzde_usulu.html", map[string]any{
		"StoreName":     storeNameOrDefault(ctx, d),
		"CanView":       canView,
		"CanRecord":     canRecord,
		"CashierFilter": cashierFilter,
		"Workers":       workers,
		"Collected":     summary.ReceivedMinor,
		"Distributed":   summary.AllocatedMinor,
		"Collections":   collections,
		"Detail":        detail,
		"From":          from,
		"To":            to,
		// LOCAL, matching both POST handlers' own future-date check
		// (workerAllocationRequestedAt) — see renderTipsTab's own note for
		// why every clock this tab touches is local, never UTC.
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

		// "yuzde_usulu_pool" (ut-docs#988) joins the two original values:
		// the SAME endpoint records a Turkey pool distribution, because
		// ADR-0063 Decision 2 is explicitly one ledger with two filtered
		// views — a second write path would be exactly how the two
		// obligations silently drift apart. The tip/service_charge request
		// shape is unchanged for existing callers: only the accepted set
		// widens, and only the new value requires the extra pool_id field
		// below.
		sourceType := strings.TrimSpace(r.FormValue("source_type"))
		if sourceType != "tip" && sourceType != "service_charge" && sourceType != "yuzde_usulu_pool" {
			http.Error(w, `source_type must be "tip", "service_charge" or "yuzde_usulu_pool"`, http.StatusBadRequest)
			return
		}

		// sourceID stays "" for tip/service_charge exactly as before (a UK
		// payout is not traced to one payment row from this form). For a
		// pool distribution it is the collection the money is coming out
		// of — ADR-0063 Decision 3's "shared source_id (a pool-batch
		// identifier, not a sales.id) linking every worker_allocations row
		// from one distribution event", which since ut-docs#988 is a real
		// yuzde_usulu_pool_collections row rather than a bare marker
		// string. Validated here (400, not 500) so a distribution can
		// never point at a pool that does not exist.
		sourceID := ""
		if sourceType == "yuzde_usulu_pool" {
			poolID := strings.TrimSpace(r.FormValue("pool_id"))
			if poolID == "" {
				http.Error(w, "pool_id is required for a yuzde_usulu_pool distribution", http.StatusBadRequest)
				return
			}
			// Distinguish "no such pool" (a 400 on an operator-supplied
			// pool_id) from a real query failure (a 500) — GetYuzdeUsuluPoolCollection's
			// own doc comment promises exactly this distinction; collapsing
			// both into one 400 would misreport a DB fault (e.g. "database
			// is locked" during a concurrent reset) as the operator's own
			// mistake, with nothing reaching the log (independent review,
			// ut-docs#988).
			_, found, err := repo.GetYuzdeUsuluPoolCollection(ctx, poolID)
			if err != nil {
				common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "reports.tips.error.save", "worker_allocation_record", err)
				return
			}
			if !found {
				http.Error(w, "pool_id must be a recorded pool collection", http.StatusBadRequest)
				return
			}
			sourceID = poolID
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

		if err := repo.InsertWorkerAllocation(ctx, tx, id, sourceType, sourceID, cashierID, amountMinor, allocatedAt, note); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "reports.tips.error.save", "worker_allocation_record", err)
			return
		}
		actorID := getSessionUserID(r)
		now := time.Now().UTC().Format(time.RFC3339)
		auditDetail := map[string]any{"source_type": sourceType, "cashier_id": cashierID, "amount_minor": amountMinor, "note": note}
		// Only present for a pool distribution — a tip/service_charge audit
		// row keeps exactly the fields it had before ut-docs#988 (sourceID
		// is always "" on those paths, so recording it would be noise on
		// every existing UK record).
		if sourceID != "" {
			auditDetail["source_id"] = sourceID
		}
		if err := repo.InsertAudit(ctx, tx, actorID, "worker_allocation", id, "worker_allocation_recorded",
			auditDetail, now, ""); err != nil {
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
		// A pool distribution is submitted from the "yuzde-usulu" tab, not
		// the "tips" tab, so it must swap that tab's own fragment back into
		// #report-tab-panel (ut-docs#988) — re-rendering the tips fragment
		// here would replace the operator's open tab with a different
		// report. tip/service_charge is untouched: same fragment, same
		// bytes, as before.
		if sourceType == "yuzde_usulu_pool" {
			renderYuzdeUsuluTab(repo, d, r, window)(w, r)
			return
		}
		renderTipsTab(repo, d, r, window)(w, r)
	})

	// POST /api/reports/worker-allocations/pool-collections records the
	// COLLECTION side of a Turkey yüzde usulü pool (ut-docs#988) — "we
	// collected X today", with deliberately NO customer-facing bill line,
	// since a Turkey service-charge line is forbidden outright
	// (ut-docs#962, common.ServiceChargeForbidden — untouched here). It is
	// a separate endpoint from the distribution POST above because it
	// writes a different table for a different event: money coming IN to
	// the pool, not going OUT to a named worker. It reuses the same
	// `worker_allocation` permission (ADR-0063 Decision 2: one ledger, two
	// filtered views — a separate permission would let the two obligations
	// drift apart) and the same date validation, so a collection can no
	// more be backdated into the future than a payout can.
	mux.HandleFunc("POST /api/reports/worker-allocations/pool-collections", func(w http.ResponseWriter, r *http.Request) {
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

		amtStr := strings.TrimSpace(r.FormValue("amount"))
		amountMinor, err := strconv.ParseInt(amtStr, 10, 64)
		if err != nil || amountMinor <= 0 {
			http.Error(w, "amount must be a positive integer (minor units)", http.StatusBadRequest)
			return
		}

		basisNote := r.FormValue("basis_note")

		collectedAt, isFuture, err := workerAllocationRequestedAt(date, time.Now())
		if err != nil {
			http.Error(w, "date must be YYYY-MM-DD", http.StatusBadRequest)
			return
		}
		if isFuture {
			http.Error(w, "date must not be in the future", http.StatusBadRequest)
			return
		}
		id := uuid.NewString()
		actorID := getSessionUserID(r)

		tx, err := d.Db.BeginTx(ctx, nil)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "reports.yuzde_usulu.error.save", "yuzde_usulu_pool_collection_record", err)
			return
		}
		defer tx.Rollback()

		// recorded_by is the session user who entered it — the collection's
		// own attribution, distinct from worker_allocations.cashier_id
		// (who a distribution was paid TO).
		if err := repo.InsertYuzdeUsuluPoolCollection(ctx, tx, id, actorID, amountMinor, collectedAt, basisNote); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "reports.yuzde_usulu.error.save", "yuzde_usulu_pool_collection_record", err)
			return
		}
		now := time.Now().UTC().Format(time.RFC3339)
		if err := repo.InsertAudit(ctx, tx, actorID, "yuzde_usulu_pool_collection", id, "yuzde_usulu_pool_collection_recorded",
			map[string]any{"amount_minor": amountMinor, "basis_note": basisNote}, now, ""); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "reports.yuzde_usulu.error.save", "yuzde_usulu_pool_collection_record", err)
			return
		}
		if err := tx.Commit(); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "reports.yuzde_usulu.error.save", "yuzde_usulu_pool_collection_record", err)
			return
		}

		// Same window-preserving re-render as the distribution POST above
		// (see its own comment for why the already-parsed form values are
		// copied onto r.URL.RawQuery): parseReportWindow/renderYuzdeUsuluTab
		// read r.URL.Query(), which is empty for this POST's body-encoded
		// fields, so without this the refreshed tab would silently reset to
		// the 14-day default instead of the window the operator had open.
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
		renderYuzdeUsuluTab(repo, d, r, window)(w, r)
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
		// yuzde_usulu_pool (ut-docs#988) is merged into this SAME export
		// rather than getting one of its own: these are rows of one ledger
		// (ADR-0063 Decision 2), the CSV already carries source_type on
		// every row to tell them apart, and a second endpoint would be a
		// second thing to keep in step with this one. Additive for existing
		// callers — a shop that has never recorded a pool gets byte-
		// identical output, since the extra rows simply do not exist.
		poolRows, err := repo.ListWorkerAllocations(ctx, from, to, cashierID, "yuzde_usulu_pool")
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "reports.tips.error.export", "worker_allocation_export", err)
			return
		}
		rows := make([]data.WorkerAllocation, 0, len(tipRows)+len(scRows)+len(poolRows))
		rows = append(rows, tipRows...)
		rows = append(rows, scRows...)
		rows = append(rows, poolRows...)
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
