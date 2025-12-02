package pages

import (
	"net/http"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

func registerIndex(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		data := map[string]any{
			"title":     "Universal Till",
			"theme":     d.State.Theme,
			"menuItems": d.Menu,
		}
		httpx.Render("ui/pages/index.html", data)(w, r)
	})
}
