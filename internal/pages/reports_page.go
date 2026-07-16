package pages

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

func registerReportsPage(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("/reports", func(w http.ResponseWriter, r *http.Request) {
		days := 14
		if v, err := strconv.Atoi(r.URL.Query().Get("days")); err == nil && v > 0 && v <= 365 {
			days = v
		}
		repo := data.NewPOSRepo(d.Db)
		daily, _ := repo.SalesByDay(r.Context(), days)
		top, _ := repo.TopItems(r.Context(), days, 10)
		methods, _ := repo.PaymentBreakdown(r.Context(), days)
		departments, _ := repo.SalesByDepartment(r.Context(), days)
		// Per-till breakdown is only meaningful once a shop runs more than one
		// register (department stores) — hide it for single-till shops.
		tills, _ := repo.SalesByTill(r.Context(), days)
		if len(tills) < 2 {
			tills = nil
		}
		archived, _ := repo.ListArchivedReports(r.Context(), 14)
		type eodRow struct {
			Period string
			Net    int64
			Sales  int
		}
		var eodRows []eodRow
		for _, a := range archived {
			if a.Kind != "eod" {
				continue
			}
			var rep data.EODReport
			if json.Unmarshal([]byte(a.Content), &rep) == nil {
				eodRows = append(eodRows, eodRow{Period: a.Period, Net: rep.Net, Sales: rep.SalesCount})
			}
		}
		eodEnabled, _, _ := d.Settings.Get(r.Context(), keyEODEnabled)
		eodTime, _, _ := d.Settings.Get(r.Context(), keyEODTime)

		var grandTotal, grandTax int64
		var grandCount int
		for _, dd := range daily {
			grandTotal += dd.Total
			grandTax += dd.TaxTotal
			grandCount += dd.Count
		}

		httpx.Render("ui/pages/reports.html", map[string]any{
			"title":       "Reports",
			"theme":       d.CurrentState().Theme,
			"menuItems":   d.Menu,
			"CanAsk":      aiService(r.Context(), d).CanAsk() && isManagerOrAuthOff(r),
			"IsManager":   isManagerOrAuthOff(r),
			"EODRows":     eodRows,
			"EODEnabled":  eodEnabled == "true",
			"EODTime":     eodTime,
			"Days":        days,
			"Daily":       daily,
			"Top":         top,
			"Departments": departments,
			"Tills":       tills,
			"Methods":     methods,
			"GrandTotal":  grandTotal,
			"GrandTax":    grandTax,
			"GrandCount":  grandCount,
		})(w, r)
	})
}
