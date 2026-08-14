package pages

import (
	"net/http"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/discovery"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// registerPendingPairingsUI wires GET /ui/tills/pending-pairings — the
// manager-facing card list backing the approve/deny UI (ADR-0033 part 3/3,
// ut-docs#185). Same direct-repo-access style as GET /ui/sync-chip
// (sync_admin.go): computed straight from PairingRepo + discovery.TillID,
// not by calling this package's own JSON API internally. The Tills page
// polls this via hx-trigger="every 30s" (ADR-0033 §4's cadence).
func registerPendingPairingsUI(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("GET /ui/tills/pending-pairings", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "sync_management") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		repo := data.NewPairingRepo(d.Db)
		list, err := repo.ListPending(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		primaryTillID, err := discovery.TillID(r.Context(), data.NewSettingsRepo(d.Db))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
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
}
