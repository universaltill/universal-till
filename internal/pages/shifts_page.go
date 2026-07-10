package pages

import (
	"net/http"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

func registerShiftsPage(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("/shifts", func(w http.ResponseWriter, r *http.Request) {
		repo := data.NewPOSRepo(d.Db)
		current, hasOpen, _ := repo.CurrentOpenShift(r.Context())
		history, _ := repo.ListRecentShifts(r.Context(), 20)
		registers, _ := repo.ListRegisters(r.Context())
		data := map[string]any{
			"title":     "Shifts",
			"theme":     d.CurrentState().Theme,
			"menuItems": d.Menu,
			"Current":   current,
			"HasOpen":   hasOpen,
			"History":   history,
			"Registers": registers,
		}
		httpx.Render("ui/pages/shifts.html", data)(w, r)
	})
}
