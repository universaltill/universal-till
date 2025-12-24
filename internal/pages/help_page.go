package pages

import (
	"net/http"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

func registerHelp(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("/help", func(w http.ResponseWriter, r *http.Request) {
		renderHelpPage(w, r, d)
	})
}

func renderHelpPage(w http.ResponseWriter, r *http.Request, d *common.Deps) {
	data := map[string]any{
		"title":     "Help",
		"theme":     d.State.Theme,
		"menuItems": d.Menu,
	}
	httpx.Render("ui/pages/help.html", data)(w, r)
}
