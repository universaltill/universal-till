package pages

import (
	"net/http"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/ui"
)

func registerDesigner(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("/designer", func(w http.ResponseWriter, r *http.Request) {
		btns, _ := d.BtnStore.Load()
		data := map[string]any{
			"title":     "Designer",
			"theme":     d.CurrentState().Theme,
			"menuItems": d.MenuSnapshot(),
			"Buttons":   ui.ToVM(btns),
		}
		httpx.Render("ui/pages/designer.html", data)(w, r)
	})
}
