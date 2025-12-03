package pages

import (
	"net/http"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/ui"
)

func registerBasket(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("/ui/basket", func(w http.ResponseWriter, r *http.Request) {
		funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
		basketView, err := ui.NewBasketView(funcs)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		b, _ := d.Engine.Scan("")
		_ = basketView.Render(w, b)
	})
}
