package pages

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// parseReportDays reads the shared ?days= window for /reports and its tab
// fragments: 1..365, anything else (missing, garbage, out of range) falls
// back to the 14-day default rather than erroring or hammering the DB with
// an unbounded window.
func parseReportDays(r *http.Request) int {
	days := 14
	if v, err := strconv.Atoi(r.URL.Query().Get("days")); err == nil && v > 0 && v <= 365 {
		days = v
	}
	return days
}

func registerReportsPage(mux *http.ServeMux, d *common.Deps) {
	// GET /reports is the always-visible monitoring section only: the KPI
	// row (revenue/sales/tax/refunds/net/YoY) and the low-stock chip. The
	// heavier reports (~16 queries previously run unconditionally,
	// ut-docs#401) moved behind /ui/reports/tab/{name} and only run when
	// the operator opens that tab.
	mux.HandleFunc("/reports", func(w http.ResponseWriter, r *http.Request) {
		days := parseReportDays(r)
		repo := data.NewPOSRepo(d.Db)
		daily, _ := repo.SalesByDay(r.Context(), days)
		curPeriod, lastYear, _ := repo.PeriodComparison(r.Context(), days)
		yoyPct := 0
		if lastYear.Total > 0 {
			yoyPct = int((curPeriod.Total - lastYear.Total) * 100 / lastYear.Total)
		}
		// Low-stock heads-up (shares the inventory page's exact decision via
		// LowStockItem.IsRunningOut — this chip links straight to
		// /inventory, so it must never disagree with what that page itself
		// warns about): a chip on the reports header so the owner sees it
		// without digging.
		runningOut := 0
		if rates, err := repo.ItemDailySellRates(r.Context(), 28); err == nil && len(rates) > 0 {
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
		grandRefunds, _, _ := repo.RefundsByWindow(r.Context(), days)
		grandNet := grandTotal - grandRefunds

		httpx.Render("ui/pages/reports.html", map[string]any{
			"title":        "Reports",
			"theme":        d.CurrentState().Theme,
			"menuItems":    d.MenuSnapshot(),
			"CanAsk":       aiService(r.Context(), d).CanAsk() && isManagerOrAuthOff(r),
			"IsManager":    isManagerOrAuthOff(r),
			"Days":         days,
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
	// enclosing /reports topic covers these fragments).
	mux.HandleFunc("/ui/reports/tab/{name}", func(w http.ResponseWriter, r *http.Request) {
		days := parseReportDays(r)
		repo := data.NewPOSRepo(d.Db)
		switch r.PathValue("name") {
		case "sales-trend":
			daily, _ := repo.SalesByDay(r.Context(), days)
			byWeekday, _ := repo.SalesByWeekday(r.Context(), days)
			byHour, _ := repo.SalesByHour(r.Context(), days)
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
			top, _ := repo.TopItems(r.Context(), days, 10)
			slow, _ := repo.SlowItems(r.Context(), days, 10)
			dead, _ := repo.DeadStock(r.Context(), days, 10)
			margins, _ := repo.MarginByItem(r.Context(), days, 10)
			httpx.RenderPartial("ui/partials/reports_tab_items.html", map[string]any{
				"Top":       top,
				"Slow":      slow,
				"DeadStock": dead,
				"Margins":   margins,
			})(w, r)
		case "tax":
			taxBands, _ := repo.TaxSummary(r.Context(), days)
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
			methods, _ := repo.PaymentBreakdown(r.Context(), days)
			departments, _ := repo.SalesByDepartment(r.Context(), days)
			// Per-till breakdown is only meaningful once a shop runs more than one
			// register (department stores) — hide it for single-till shops.
			tills, _ := repo.SalesByTill(r.Context(), days)
			if len(tills) < 2 {
				tills = nil
			}
			httpx.RenderPartial("ui/partials/reports_tab_payments.html", map[string]any{
				"Methods":     methods,
				"Departments": departments,
				"Tills":       tills,
			})(w, r)
		case "eod":
			// Manager-gated like every other manager-only handler in this
			// codebase (isManagerOrAuthOff before the repo calls, not just in
			// the template): the partial only ever renders its body
			// {{ if .IsManager }}, so a non-manager gets nothing back either
			// way — but pre-this-fix, ListArchivedReports and two Settings.Get
			// calls still ran for a role that can never see the result.
			isManager := isManagerOrAuthOff(r)
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
				"IsManager":  isManager,
				"EODRows":    eodRows,
				"EODEnabled": eodEnabled == "true",
				"EODTime":    eodTime,
			})(w, r)
		default:
			http.NotFound(w, r)
		}
	})
}
