package pages

import (
	"encoding/json"
	"net/http"

	"github.com/universaltill/universal-till/internal/netreach"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// registerNetStatus: GET /ui/net-status, polled by the status-bar script in
// base.html every 10 s (ut-docs#3095). It answers from netreach.Monitor's
// cache and never waits on the network:
//
//	{"data":{"cloud_reachable":true|false|null},"error":null}
//
// null = unknown (no probe yet, or disabled: no endpoint, or a loopback
// dev/e2e endpoint). The light shows "No internet" on false; checkout never
// reads this (ADR-0044 D1 — the sale's offline flag stays navigator.onLine).
func registerNetStatus(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("GET /ui/net-status", netStatusHandler(d.NetReach.Status))
}

func netStatusHandler(status func() netreach.State) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":  map[string]any{"cloud_reachable": status().Reachable()},
			"error": nil,
		})
	}
}
