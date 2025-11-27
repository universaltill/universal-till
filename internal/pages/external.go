package pages

import (
	"io"
	"net/http"
	"strings"
)

// registerExternalProxy proxies external menu plugins defined in the plugin manager.
func registerExternalProxy(mux *http.ServeMux, d *deps) {
	mux.HandleFunc("/ext/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/ext/"), "/")
		if len(parts) == 0 || parts[0] == "" {
			http.NotFound(w, r)
			return
		}
		pid := parts[0]
		mp := d.pm.MenuPlugins[pid]
		if mp.URL == "" {
			http.NotFound(w, r)
			return
		}
		resp, err := http.Get(mp.URL)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.Copy(w, resp.Body)
	})
}
