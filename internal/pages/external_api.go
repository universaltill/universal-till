package pages

import (
	"io"
	"net/http"
	"strings"

	"github.com/universaltill/universal-till/internal/pages/common"
)

// registerExternalProxy proxies external menu plugins defined in the plugin manager.
func registerExternalProxy(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("/ext/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/ext/"), "/")
		if len(parts) == 0 || parts[0] == "" {
			http.NotFound(w, r)
			return
		}
		pid := parts[0]
		mp, ok := d.MenuPluginByKey(pid)
		if !ok || mp.Route == "" {
			http.NotFound(w, r)
			return
		}
		resp, err := http.Get(mp.Route)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.Copy(w, resp.Body)
	})
}
