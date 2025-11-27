package pages

import (
	"net/http"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/plugins"
)

type SettingPage struct {
	data map[string]any
}

// handle implements IPage.
func (p SettingPage) handle(cfg *config.Config, pm *plugins.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// cfg := &settings.RuntimeConfig{
		// 	StoreName:    r.Form.Get("storeName"),
		// 	Currency:     r.Form.Get("currency"),
		// 	TaxInclusive: r.Form.Get("taxInclusive") == "on",
		// }
		// if err := s.SaveRuntimeConfig(r.Context(), cfg); err != nil {
		// 	http.Error(w, err.Error(), http.StatusInternalServerError)
		// 	return
		// }
		// cur := settings.GetAll()

		httpx.Render("ui/pages/settings.html", p.data)(w, r)
	}
}
