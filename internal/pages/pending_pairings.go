package pages

import (
	"net/http"
	"strings"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/discovery"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// registerPendingPairingsUI wires GET /ui/tills/pending-pairings — the
// manager-facing card list backing the approve/deny UI (ADR-0033 part 3/3,
// ut-docs#185) — and GET /ui/pairing-notice, the nav-level dismissible
// notice for the same queue (ut-docs#1551). Same direct-repo-access style
// as GET /ui/sync-chip (sync_admin.go): computed straight from
// PairingRepo + discovery.TillID, not by calling this package's own JSON
// API internally. The Tills page polls the first via
// hx-trigger="every 30s" (ADR-0033 §4's cadence); base.html polls the
// second the same way on every page.
func registerPendingPairingsUI(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("GET /ui/tills/pending-pairings", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "sync_management") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		repo := data.NewPairingRepo(d.Db)
		list, err := repo.ListPending(r.Context())
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "pairings.error.server", "pairings", err)
			return
		}
		primaryTillID, err := discovery.TillID(r.Context(), data.NewSettingsRepo(d.Db))
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "pairings.error.server", "pairings", err)
			return
		}
		type row struct {
			ID               string
			DeviceName       string
			VerificationCode string
		}
		rows := make([]row, 0, len(list))
		for _, p := range list {
			rows = append(rows, row{
				ID:               p.ID,
				DeviceName:       p.DeviceName,
				VerificationCode: derivedVerificationCode(p.Commitment, primaryTillID),
			})
		}
		httpx.RenderPartial("ui/partials/pending_pairings.html", map[string]any{"Pending": rows})(w, r)
	})

	// GET /ui/pairing-notice (ut-docs#1551): a manager anywhere in the app
	// — not just on /tills — sees a pending pairing request within one
	// polling interval, mirroring the sync/bugreport/session/fiscal chips'
	// own "nav.html has no per-request data, load a fragment after render"
	// convention (base.html mounts the placeholder). Pairing approval only
	// ever happens on the primary (a replica has no PairingRepo rows of its
	// own to approve), so this skips the same way GET /ui/sync-chip's
	// replica branch does (sync_admin.go) rather than relying on
	// canPerform alone — a cashier or manager working on a REPLICA till
	// must never see a notice about a request only the primary can act on.
	mux.HandleFunc("GET /ui/pairing-notice", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "sync_management") || d.SyncPrimaryURL(r.Context()) != "" {
			w.WriteHeader(http.StatusOK)
			return
		}
		// Read-only variant (no opportunistic expired-row DELETE): this
		// fragment is polled every 30s from every open page by every
		// manager, unlike the approve/deny queue this repo method was
		// originally written for -- see ListPendingReadOnly's own comment
		// (independent review, ut-docs#1551).
		repo := data.NewPairingRepo(d.Db)
		list, err := repo.ListPendingReadOnly(r.Context())
		if err != nil {
			// Same "silent empty on error" convention as the sibling nav
			// fragments (GET /ui/sync-chip, GET /ui/bugreport-chip) rather
			// than a 500: htmx doesn't swap a non-2xx response anyway (the
			// placeholder just keeps its last good content), so the only
			// visible effect of a 500 here is logging an error every 30s
			// per open page for as long as the underlying read stays
			// broken (independent review).
			logging.L().Errorf("list pending pairings (read-only): %v", err)
			w.WriteHeader(http.StatusOK)
			return
		}
		if len(list) == 0 {
			w.WriteHeader(http.StatusOK)
			return
		}
		// Fingerprint is the sorted-by-request-time-then-id (stable across
		// polls even for two requests landing in the same second --
		// independent review) set of pending IDs -- the client-side
		// dismiss script (pairing_notice.html) compares this against what
		// was last dismissed, so a resolved request (approved/denied/
		// expired, which ListPendingReadOnly already excludes) or a
		// genuinely NEW one changes the fingerprint and the notice
		// reappears, satisfying "must clear itself... never a stale
		// notice" with no extra state kept here.
		ids := make([]string, 0, len(list))
		for _, p := range list {
			ids = append(ids, p.ID)
		}
		httpx.RenderPartial("ui/partials/pairing_notice.html", map[string]any{
			"Count":       len(list),
			"Fingerprint": strings.Join(ids, ","),
		})(w, r)
	})
}
