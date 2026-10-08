package pages

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/fleetlink"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// "Update now" on the main till's Tills page (ut-docs#2945, ADR-0114 §5,
// LAN half): asks one linked till to install the main till's own version
// at its next safe moment, even when the shop switched automatic updates
// off. The request is a fleet frame naming that version — nothing more
// (§7); the replica's follow rule still decides (no open sale, an install
// that can replace itself, never a dev build or a downgrade) and downloads
// the checksum-verified release itself, never from the main till.

// registerTillUpdateNow wires POST /api/tills/{id}/update-now.
func registerTillUpdateNow(mux *http.ServeMux, d *common.Deps) {
	tills := data.NewTillsRepo(d.Db)
	posRepo := data.NewPOSRepo(d.Db)

	// Same manager gate as every sibling on the Tills page
	// (sync_management) and, like Change role and Revoke, main till only:
	// only the main till has the link hub that reaches the other tills.
	mux.HandleFunc("POST /api/tills/{id}/update-now", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if !canPerform(d, r, "sync_management") {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
			return
		}
		if d.SyncPrimaryURL(ctx) != "" {
			http.Error(w, "update a till from the main till", http.StatusConflict)
			return
		}
		id := r.PathValue("id")
		_, found, err := tills.RoleByID(ctx, id)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "sync.error.server", "till_update_now", err)
			return
		}
		if !found {
			http.Error(w, "till not found", http.StatusNotFound)
			return
		}
		locale := httpx.ResolveLocale(w, r)
		target := buildinfo.Version
		// A dev build names no release a till could download.
		if !releaseVersion(target) {
			httpx.RefuseText(w, httpx.T(locale, "tills.update_now.dev_build"), http.StatusConflict)
			return
		}
		if d.Link == nil {
			httpx.RefuseText(w, httpx.T(locale, "tills.update_now.not_linked"), http.StatusConflict)
			return
		}
		if err := d.Link.RequestUpdate(id, target); err != nil {
			if errors.Is(err, fleetlink.ErrNotLinked) {
				httpx.RefuseText(w, httpx.T(locale, "tills.update_now.not_linked"), http.StatusConflict)
				return
			}
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "sync.error.server", "till_update_now", err)
			return
		}
		// Best-effort like every settings audit: the request already went.
		if err := posRepo.InsertAudit(ctx, nil, settingsActorID(r), "till", id, "update_requested",
			map[string]any{"target": target}, time.Now().UTC().Format(time.RFC3339), ""); err != nil {
			logging.L().Errorf("till update-now: audit update_requested for %s failed: %v", id, err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":  map[string]string{"till_id": id, "target": target},
			"error": nil,
		})
	})
}
