package pages

import (
	"net/http"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// registerBackofficePage serves the manager dashboard a back-office device
// lands on (ADR-0018: "/" redirects here when display.mode=backoffice, and
// any till can visit it directly). It is a glance surface — today vs
// yesterday, the week, what's running low, what went wrong — with links into
// the full pages; the heavy analysis stays on /reports.
func registerBackofficePage(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("/backoffice", func(w http.ResponseWriter, r *http.Request) {
		repo := data.NewPOSRepo(d.Db)
		todayTotal, todayCount, _ := repo.DayTotal(r.Context(), 0)
		ydayTotal, ydayCount, _ := repo.DayTotal(r.Context(), 1)

		var weekTotal int64
		var weekCount int
		if daily, err := repo.SalesByDay(r.Context(), 7); err == nil {
			for _, day := range daily {
				weekTotal += day.Total
				weekCount += day.Count
			}
		}

		low, _ := repo.GetLowStockItems(r.Context(), "")
		if len(low) > 8 {
			low = low[:8]
		}

		problems := logging.Recent()
		if len(problems) > 5 {
			problems = problems[:5]
		}

		httpx.Render("ui/pages/backoffice.html", map[string]any{
			"title":      "Back office",
			"theme":      d.CurrentState().Theme,
			"menuItems":  d.Menu,
			"TodayTotal": todayTotal,
			"TodayCount": todayCount,
			"YdayTotal":  ydayTotal,
			"YdayCount":  ydayCount,
			"WeekTotal":  weekTotal,
			"WeekCount":  weekCount,
			"LowStock":   low,
			"Problems":   problems,
		})(w, r)
	})
}
