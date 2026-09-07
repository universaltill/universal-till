package pages

import (
	"net/http"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Cross-till table-claim WRITE-THROUGH, primary side (ut-docs#1703) — the
// "harder half" registerSyncTables's doc comment (sync_tables.go) split out
// of the read-only occupancy proxy (ut-docs#1392). Before this, two tills
// could each claim the same table: table_claims is local-only (excluded from
// the admin-bundle sync by name, sync_admin_repo.go), so nothing ever
// proxied a replica's ClaimTable/ReleaseTableClaim to the primary. Now a
// replica's claimTableWriteThrough / releaseTableClaimWriteThrough
// (tables_claim_proxy.go) POST here first, and the PRIMARY's table_claims —
// one row per table, owned by a tills.id — is the single shop-wide
// arbiter while reachable. Same shape as registerSyncOrders: bearer-authed
// via syncTill, JSON envelope { "data": …, "error": null }, snake_case.
//
// Orphan reconciliation: a replica that crashes or loses the network after
// claiming can't release, so every claim is TTL-reconciled against its
// owning till's tills.last_seen_at (which syncTill touches on every call —
// a replica that is still talking to the primary keeps its claims alive
// implicitly, no heartbeat of its own needed). See POSRepo.ClaimTableForTill.
//
// Auth-middleware note: both paths must be on internal/auth/middleware.go's
// exempt list (TestSyncPullPathsAreExempt pins them), or the replica is
// 401'd before syncTill ever runs and the proxy silently falls back to
// local-only — the /api/sync/stock failure class.

// tillClaimTTL is how long a till may go unseen by the primary before its
// table claims are considered orphaned and may be taken over by another
// till. Deliberately the SAME 2-minute bound sync_admin.go's status chip
// uses — `withinLast(t.LastSeenAt, 2*time.Minute)` — to call a till
// offline: one decision, "is this till online", made once, so the chip's
// "warn" and a claim's expiry can never disagree about the same till. The
// replica sync loops touch last_seen_at far more often than that (the
// journal push nudges within seconds of a sale; pulls run on their own
// cadence), so a healthy till's claims are never at risk.
const tillClaimTTL = 2 * time.Minute

// syncTableClaimResult is the wire form of a claim outcome.
type syncTableClaimResult struct {
	Claimed bool `json:"claimed"`
}

// syncTableReleaseResult is the wire form of a release outcome — always
// released=true on a 200: release is idempotent and scoped to the caller's
// own claim, so there is no failure to report beyond auth/validation.
type syncTableReleaseResult struct {
	Released bool `json:"released"`
}

// registerSyncTablesClaim mounts the primary-side claim/release endpoints on
// the bearer-authed /api/sync/* surface, next to registerSyncTables's GET.
func registerSyncTablesClaim(mux *http.ServeMux, d *common.Deps) {
	tills := data.NewTillsRepo(d.Db)
	posRepo := data.NewPOSRepo(d.Db)

	// Claim table_id for the calling till. 200 with claimed=false is the
	// ordinary "someone else has it" answer (the replica renders the same
	// occupied toast as a local race), never an error status.
	mux.HandleFunc("POST /api/sync/tables/claim", func(w http.ResponseWriter, r *http.Request) {
		till, ok := syncTill(r, tills)
		if !ok {
			writeSyncOrdersJSON(w, http.StatusUnauthorized, nil, "unauthorized")
			return
		}
		_ = r.ParseForm()
		tableID := strings.TrimSpace(r.Form.Get("table_id"))
		if tableID == "" {
			writeSyncOrdersJSON(w, http.StatusBadRequest, nil, "table_id required")
			return
		}
		claimed, err := posRepo.ClaimTableForTill(r.Context(), tableID, till.ID, time.Now().Add(-tillClaimTTL))
		if err != nil {
			logging.L().Errorf("sync table claim %s from %s: %v", tableID, till.Name, err)
			writeSyncOrdersJSON(w, http.StatusInternalServerError, nil, "server error")
			return
		}
		writeSyncOrdersJSON(w, http.StatusOK, syncTableClaimResult{Claimed: claimed}, nil)
	})

	// Release the calling till's own claim on table_id. Idempotent — a
	// release with nothing to release is still 200/released.
	mux.HandleFunc("POST /api/sync/tables/release", func(w http.ResponseWriter, r *http.Request) {
		till, ok := syncTill(r, tills)
		if !ok {
			writeSyncOrdersJSON(w, http.StatusUnauthorized, nil, "unauthorized")
			return
		}
		_ = r.ParseForm()
		tableID := strings.TrimSpace(r.Form.Get("table_id"))
		if tableID == "" {
			writeSyncOrdersJSON(w, http.StatusBadRequest, nil, "table_id required")
			return
		}
		if err := posRepo.ReleaseTableClaimForTill(r.Context(), tableID, till.ID); err != nil {
			logging.L().Errorf("sync table release %s from %s: %v", tableID, till.Name, err)
			writeSyncOrdersJSON(w, http.StatusInternalServerError, nil, "server error")
			return
		}
		writeSyncOrdersJSON(w, http.StatusOK, syncTableReleaseResult{Released: true}, nil)
	})
}
