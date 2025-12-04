package pages

import (
	"net/http"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

func registerInventoryPage(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("/inventory", func(w http.ResponseWriter, r *http.Request) {
		data := map[string]any{
			"title":     "Inventory",
			"theme":     d.State.Theme,
			"menuItems": d.Menu,
		}
		httpx.Render("ui/pages/inventory.html", data)(w, r)
	})
}
