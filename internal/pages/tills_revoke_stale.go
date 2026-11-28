package pages

import (
	"net/http"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/discovery"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/tills"
)

// registerTillsRevokeStale wires POST /api/sync/tills/revoke-stale: the
// "Remove all tills not in your account" button (ut-docs#7057). Manager
// only, main till only, and only while the account list can be read right
// now (a fresh fetch, never the cache): an unknown list never removes
// anything. Each removal is the same revoke as the one-tap Remove, audited
// as till_revoked.
func registerTillsRevokeStale(mux *http.ServeMux, d *common.Deps) {
	repo := data.NewTillsRepo(d.Db)
	posRepo := data.NewPOSRepo(d.Db)
	mux.HandleFunc("POST /api/sync/tills/revoke-stale", func(w http.ResponseWriter, r *http.Request) {
		if d.SyncPrimaryURL(r.Context()) != "" {
			http.Error(w, "revoke must be done on the primary till", http.StatusConflict)
			return
		}
		// Cheap refusal first, so a replica never burns lockout attempts.
		actor, ok := authorizeTillRemoval(w, r, d)
		if !ok {
			return
		}
		devs, ok := accountFleet(r.Context(), d, true)
		if !ok {
			common.LocalizedError(w, r, http.StatusConflict, "tills.list.error.account_unavailable")
			return
		}
		list, err := repo.ListTills(r.Context())
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "sync.error.server", "tills_revoke_stale", err)
			return
		}
		selfID, _ := discovery.ReportedTillID(r.Context(), d.Settings)
		rows := tills.Merge(tills.Source{Cloud: devs, CloudOK: true, Local: list,
			Self: tills.SelfInfo{ID: selfID, Role: tills.RoleMain}}, time.Now(), tillsStaleAfter)
		// Only the tills the manager was shown and confirmed (the form posts
		// their ids), and only those still stale AND unseen past the window:
		// a till seen lately is never bulk-removed.
		_ = r.ParseForm()
		confirmed := map[string]bool{}
		for _, id := range r.Form["id"] {
			confirmed[id] = true
		}
		for _, row := range rows {
			if (row.State != tills.StateLocalOnly && row.State != tills.StateOldEntry) || row.IsSelf || !row.LongUnseen || !confirmed[row.ID] {
				continue
			}
			if err := revokeTillAndRetireRegister(r.Context(), repo, posRepo, row.ID, data.RetiredOldEntry, actor); err != nil {
				common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "sync.error.server", "tills_revoke_stale", err)
				return
			}
			if d.Link != nil {
				d.Link.Disconnect(row.ID)
			}
			_ = posRepo.InsertAudit(r.Context(), nil, actor, "till", row.ID, "till_revoked",
				map[string]any{"reason": "not_in_account"}, time.Now().UTC().Format(time.RFC3339), "")
		}
		w.Header().Set("HX-Trigger", "tills-changed")
		w.WriteHeader(http.StatusNoContent)
	})
}
