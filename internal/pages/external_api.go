package pages

import (
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/pages/common"
)

// externalProxyClient carries a bounded timeout so a hung external
// menu-plugin host can't pin the handling goroutine indefinitely
// (ut-docs#912) — same 60s bound used elsewhere for outbound HTTP, e.g.
// sync_admin.go's StartSyncPull client.
var externalProxyClient = &http.Client{Timeout: 60 * time.Second}

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
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, mp.Route, nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		resp, err := externalProxyClient.Do(req)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusBadGateway, "ext.error.unreachable", "ext_proxy", err)
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.Copy(w, resp.Body)
	})
}
