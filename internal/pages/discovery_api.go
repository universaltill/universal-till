package pages

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/universaltill/universal-till/internal/discovery"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// LAN till discovery (ADR-0033 part 1, universaltill/ut-docs#264): a manager
// on the Tills page can ask "is there a primary already on this network?"
// on demand — a bounded, per-click scan, never an ambient/background
// browser. Selecting a result to actually join is a separate future card
// (#185); this endpoint only surfaces read-only candidates.

// discoverBrowseTimeout bounds the LAN scan this endpoint runs per request.
const discoverBrowseTimeout = 4 * time.Second

// discoveryBrowse is a package var (not a direct discovery.Browse call) so
// tests can substitute a fast fake instead of waiting out a real, bounded
// multi-second network scan on every run.
var discoveryBrowse = discovery.Browse

// registerDiscoveryAPI wires GET /api/sync/discover-primaries. Gated the
// same way as the rest of the Tills page's API surface (isManagerOrAuthOff,
// see sync_api.go's /api/sync/enroll-token and pairing endpoints) — not a
// stricter or laxer check than its neighbours.
func registerDiscoveryAPI(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("GET /api/sync/discover-primaries", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			http.Error(w, "manager or admin required", http.StatusForbidden)
			return
		}
		candidates, err := discoveryBrowse(r.Context(), discoverBrowseTimeout)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if candidates == nil {
			candidates = []discovery.Candidate{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":  map[string]any{"primaries": candidates},
			"error": nil,
		})
	})
}
