package pages

import (
	"net/http"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/plugins"
)

type IndexPage struct {
	data map[string]any
}

// handle implements IPage.
func (p IndexPage) handle(cfg *config.Config, pm *plugins.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		httpx.Render("ui/pages/index.html", p.data)(w, r)
	}
}
