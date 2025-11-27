package pages

import (
	"net/http"

	"github.com/universaltill/universal-till/internal/httpx"
)

func registerPluginsPage(mux *http.ServeMux, d *deps) {
	mux.HandleFunc("/plugins", func(w http.ResponseWriter, r *http.Request) {
		installed := []string{}
		for id := range d.pm.MenuPlugins {
			installed = append(installed, id)
		}
		data := map[string]any{
			"title":         "Plugins",
			"theme":         d.state.Theme,
			"menuItems":     d.menu,
			"installedIDs":  installed,
			"downloadedIDs": []string{}, // DB-backed plugins; no local bundle tracking
		}
		httpx.Render("ui/pages/plugins.html", data)(w, r)
	})
}
