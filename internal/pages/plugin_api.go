package pages

import (
	"net/http"
	"strings"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
)

func registerPluginAPI(mux *http.ServeMux, d *common.Deps) {
	repo := data.NewPluginRepo(d.Db)
	mux.HandleFunc("/api/plugins/install", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		id := strings.TrimSpace(r.Form.Get("id"))
		if id == "" {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		if err := repo.InstallPlugin(r.Context(), id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = d.Pm.Reload(r.Context())
		d.Menu = common.BuildMenu(d.BaseMenu, d.Pm)
		w.WriteHeader(http.StatusNoContent)
	})
}
