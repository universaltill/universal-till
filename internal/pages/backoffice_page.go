package pages

import (
	"net/http"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
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
		if !canPerform(d, r, "reports") {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required") // page-error:allow ut-docs#1458 (pending migration to httpx.RenderError — tracked follow-up card, out of #1455's scope)
			return
		}
		repo := data.NewPOSRepo(d.Db)
		dayRef := time.Now()
		todayTotal, todayCount, _ := repo.DayTotal(r.Context(), 0, dayRef)
		ydayTotal, ydayCount, _ := repo.DayTotal(r.Context(), 1, dayRef)

		var weekTotal int64
		var weekCount int
		// +1s pad: SQL window comparisons truncate to whole seconds, so an
		// exact-now exclusive upper bound can drop a sale committed in this
		// same wall-clock second (see reportNow's doc comment in
		// reports_page.go).
		weekNow := dayRef.Add(time.Second) // ut-docs#969 review (N4): share dayRef rather than a second time.Now() read
		// 0, 0 (calendar-local-midnight grouping): this widget only sums
		// day.Total/day.Count across every returned row for a weekly
		// aggregate — it never displays the per-row Day label — so which
		// boundary the rows are grouped by cannot change the sum over this
		// fixed [from, to) window (a sum over any partition of the same
		// window is identical). Unaffected by ut-docs#559, not just unfixed.
		if daily, err := repo.SalesByDay(r.Context(), weekNow.Add(-7*24*time.Hour), weekNow, 0, 0); err == nil {
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
