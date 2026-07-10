package pages

import (
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

		var grandTotal, grandTax int64
		var grandCount int
		for _, dd := range daily {
			grandTotal += dd.Total
			grandTax += dd.TaxTotal
			grandCount += dd.Count
		}

		httpx.Render("ui/pages/reports.html", map[string]any{
			"title":      "Reports",
			"theme":      d.State.Theme,
			"menuItems":  d.Menu,
			"Days":       days,
			"Daily":      daily,
			"Top":        top,
			"Methods":    methods,
			"GrandTotal": grandTotal,
			"GrandTax":   grandTax,
			"GrandCount": grandCount,
		})(w, r)
	})
}
