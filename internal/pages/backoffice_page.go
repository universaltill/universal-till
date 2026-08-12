package pages

import (
	"net/http"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/reportperiod"
)

// registerBackofficePage serves the manager dashboard a back-office device
// lands on (ADR-0018: "/" redirects here when display.mode=backoffice). Any
// till can visit it directly — single source of truth stays the shop's
// primary/replica database (ADR-0011); backoffice is a manager/admin ROLE
// gate, not a device lock, so whoever has the role can open it from
// whichever till they're standing at. It is a glance surface — today vs
// yesterday, the week, what's running low, what went wrong — with links into
// the full pages; the heavy analysis stays on /reports.
func registerBackofficePage(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("/backoffice", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			http.Error(w, "manager or admin role required", http.StatusForbidden)
			return
		}
		repo := data.NewPOSRepo(d.Db)
		todayTotal, todayCount, _ := repo.DayTotal(r.Context(), 0)
		ydayTotal, ydayCount, _ := repo.DayTotal(r.Context(), 1)

		var weekTotal int64
		var weekCount int
		weekStart, weekEnd := reportperiod.RollingWindow(7, time.Now())
		if daily, err := repo.SalesByDay(r.Context(), weekStart, weekEnd); err == nil {
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
			"menuItems":  d.MenuSnapshot(),
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
