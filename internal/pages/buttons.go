package pages

import (
	"net/http"
	"path/filepath"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/ui"
)

func registerButtonsAPI(mux *http.ServeMux, d *deps) {
	// UI fragment
	mux.HandleFunc("/ui/buttons", func(w http.ResponseWriter, r *http.Request) {
		funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
		renderer, err := ui.NewRenderer(
			filepath.Join("web", "ui", "layouts", "base.html"),
			filepath.Join("web", "ui", "pages", "index.html"),
			filepath.Join("web", "ui", "partials", "buttons.html"),
			funcs,
		)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		btnHTTP := &ui.ButtonsHTTP{Store: *d.btnStore, View: renderer}
		btnHTTP.List(w, r)
	})

	// Admin add/remove
	mux.HandleFunc("/api/buttons/add", func(w http.ResponseWriter, r *http.Request) {
		funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
		renderer, err := ui.NewRenderer(
			filepath.Join("web", "ui", "layouts", "base.html"),
			filepath.Join("web", "ui", "pages", "index.html"),
			filepath.Join("web", "ui", "partials", "buttons_admin.html"),
			funcs,
		)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		btnHTTP := &ui.ButtonsHTTP{Store: *d.btnStore, View: renderer}
		btnHTTP.Add(w, r)
	})

	mux.HandleFunc("/api/buttons/remove", func(w http.ResponseWriter, r *http.Request) {
		funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
		renderer, err := ui.NewRenderer(
			filepath.Join("web", "ui", "layouts", "base.html"),
			filepath.Join("web", "ui", "pages", "index.html"),
			filepath.Join("web", "ui", "partials", "buttons_admin.html"),
			funcs,
		)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		btnHTTP := &ui.ButtonsHTTP{Store: *d.btnStore, View: renderer}
		btnHTTP.Remove(w, r)
	})
}
