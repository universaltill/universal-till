package pages

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/discovery"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/lantls"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// "Change network key" on the main till's Tills page (ut-docs#4423,
// ADR-0114 §7's on-demand trigger): starts the same LAN TLS key rotation
// the 4-year step does. The next key is announced as next_pin in a fresh
// hello on every live link right away; the switch rules are unchanged —
// at a start once every linked till confirmed it, or 14 days on — and a
// till that missed it re-proves (ut-docs architecture/lan-sync.md,
// "Missed peers").

// registerTillRotateTLSKey wires POST /api/tills/rotate-tls-key.
func registerTillRotateTLSKey(mux *http.ServeMux, d *common.Deps) {
	settingsRepo := data.NewSettingsRepo(d.Db)
	posRepo := data.NewPOSRepo(d.Db)

	// Same manager gate as every sibling on the Tills page
	// (sync_management) and main till only: the key is the main till's
	// own, and only its link hub reaches the other tills.
	mux.HandleFunc("POST /api/tills/rotate-tls-key", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if !canPerform(d, r, "sync_management") {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
			return
		}
		if d.SyncPrimaryURL(ctx) != "" {
			http.Error(w, "change the network key on the main till", http.StatusConflict)
			return
		}
		locale := httpx.ResolveLocale(w, r)
		pin, started, err := lantls.Serving().RotateNow()
		if errors.Is(err, lantls.ErrNoTLS) {
			httpx.RefuseText(w, httpx.T(locale, "tills.network_key.no_tls"), http.StatusConflict)
			return
		}
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "sync.error.server", "till_rotate_tls_key", err)
			return
		}
		// A second press resends too: a link that missed the first hello
		// (a full queue, a till that linked a moment later) catches up
		// without waiting for its next reconnect.
		told := 0
		if d.Link != nil {
			told = d.Link.ResendHello(ctx)
		}
		if started {
			// Best-effort like every settings audit: the key exists already.
			id, idErr := discovery.TillID(ctx, settingsRepo)
			if idErr != nil {
				logging.L().Warnf("till rotate-tls-key: this till's id: %v", idErr)
			}
			if err := posRepo.InsertAudit(ctx, nil, settingsActorID(r), "till", id, "lan_tls_rotation_started",
				map[string]any{"next_pin": pin}, time.Now().UTC().Format(time.RFC3339), ""); err != nil {
				logging.L().Errorf("till rotate-tls-key: audit lan_tls_rotation_started failed: %v", err)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":  map[string]any{"next_pin": pin, "started": started, "linked_tills_told": told},
			"error": nil,
		})
	})
}
