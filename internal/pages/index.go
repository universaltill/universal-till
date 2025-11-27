package pages

import (
	"net/http"

	"github.com/universaltill/universal-till/internal/httpx"
)

func registerIndex(mux *http.ServeMux, d *deps) {
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		data := map[string]any{
			"title":     "Universal Till",
			"theme":     d.state.Theme,
			"menuItems": d.menu,
		}
		httpx.Render("ui/pages/index.html", data)(w, r)
	})
}
