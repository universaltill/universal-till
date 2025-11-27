package pages

import (
	"net/http"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/ui"
)

func registerDesigner(mux *http.ServeMux, d *deps) {
	mux.HandleFunc("/designer", func(w http.ResponseWriter, r *http.Request) {
		btns, _ := d.btnStore.Load()
		data := map[string]any{
			"title":     "Designer",
			"theme":     d.state.Theme,
			"menuItems": d.menu,
			"Buttons":   ui.ToVM(btns),
		}
		httpx.Render("ui/pages/designer.html", data)(w, r)
	})
}
