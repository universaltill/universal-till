package pages

import (
	"encoding/json"
	"log"
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

// registerDiscoveryAPI wires the two flavours of the same scan
// (ut-docs#289): GET /api/sync/discover-primaries for a manager on the
// Tills page (managerGate/sync_management, same as sync_api.go's
// /api/sync/enroll-token and pairing endpoints — not a stricter or laxer
// check than its neighbours), and GET /api/setup/discover-primaries for the
// first-boot wizard's "Join an existing shop" step, gated by NeedsFirstBoot
// instead (middleware-exempt; see firstBootGate).
func registerDiscoveryAPI(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("GET /api/sync/discover-primaries", discoverPrimariesHandler(managerGate(d)))
	// Rate-limited (ut-docs#289 review): unlike its manager-gated sibling
	// above, this flavour needs no session at all — any LAN host can trigger
	// a 4s mDNS multicast scan per request during the first-boot window.
	setupDiscoverLimiter := newPairRateLimiter(time.Minute, 5)
	mux.HandleFunc("GET /api/setup/discover-primaries", discoverPrimariesHandler(rateLimited(setupDiscoverLimiter, firstBootGate(d))))
}

// discoverPrimariesHandler runs the bounded LAN scan and reports the
// candidates found — the shared logic behind both routes above. Discovery
// only PRESENTS candidates; nothing here (or in either gate) enrols
// anything, that always takes the explicit pair-start + primary-side
// approval that follows.
func discoverPrimariesHandler(gate apiGate) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !gate(w, r) {
			return
		}
		candidates, err := discoveryBrowse(r.Context(), discoverBrowseTimeout)
		if err != nil {
			// Never put the raw driver/network error in the response
			// (ut-docs#303's standing rule, and ut-docs#538's own AC): log
			// it server-side where a real diagnosis needs it, and give the
			// client a generic marker — the UI's own "tills.discovery.error"
			// i18n string is what the operator actually sees (fetch's
			// .then(r => r.json()) fails to parse this plain-text body and
			// falls into the page's .catch(), same as before this change).
			log.Printf("[discovery] LAN scan failed: %v", err)
			http.Error(w, "discovery scan failed", http.StatusInternalServerError)
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
	}
}
