package pages

import (
	"net/http"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

func registerShiftsPage(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("/shifts", func(w http.ResponseWriter, r *http.Request) {
		data := map[string]any{
			"title":     "Shifts",
			"theme":     d.State.Theme,
			"menuItems": d.Menu,
		}
		httpx.Render("ui/pages/shifts.html", data)(w, r)
	})
}
