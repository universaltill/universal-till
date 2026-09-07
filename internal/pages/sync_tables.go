package pages

import (
	"net/http"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Cross-till table occupancy, DISPLAY only (ut-docs#1392): the primary-side
// read surface a replica's tablesWithStateForDisplay (tables_sync_proxy.go)
// proxies to. `tables` (the floor plan itself) IS part of the admin-bundle
// sync (sync_admin_repo.go's adminTables, ut-docs#1546) — but table_claims
// and held_sales, the two sources ListTablesWithState derives Occupied
// from, are deliberately NOT (table_claims is called out there by name as
// "ephemeral... never meant to survive a periodic snapshot"). held_sales
// itself STILL isn't synced or proxied cross-till at all — a replica's
// parked order stays local-only, invisible even to the primary's own
// held_sales table. What travels instead, since ut-docs#1704, is the
// TABLE OCCUPANCY it implies: hold_api.go's hold handler now keeps (rather
// than releases) the table_claims row the order's table was originally
// picked with, reusing the exact write-through + TTL-reconciliation
// mechanism ut-docs#1703 built for the live basket — so a held order needs
// no new proxy call of its own, it only had to stop throwing away the one
// its table pick already made. So occupancy genuinely doesn't travel any
// other way — same shape as registerSyncOrders, the PRIMARY's own DB is a
// live source of truth for occupancy a replica reads through while
// reachable, MERGED with (never replacing) its own local occupancy — see
// tablesWithStateForDisplay's own doc comment for why a replica can never
// simply defer to the primary's answer here.
//
// Deliberately READ-ONLY — no write endpoint here. Actually PREVENTING a
// cross-till double-claim needs ClaimTable/ReleaseTableClaim to write
// through to the primary, which in turn needs an owning-till-id + TTL
// reconciliation scheme so a replica that crashes/loses network mid-claim
// doesn't leave an orphaned claim on the primary forever (nothing today
// would ever clean one up — POSRepo.ClearLocalTableClaims only runs at a
// till's own boot and only clears that till's own local rows). That's
// split out as ut-docs#1703, its own Architect pass; this endpoint only
// ever reads.
func registerSyncTables(mux *http.ServeMux, d *common.Deps) {
	tills := data.NewTillsRepo(d.Db)
	posRepo := data.NewPOSRepo(d.Db)

	// The primary's live table occupancy — the same rows ListTablesWithState
	// feeds the local floor-plan tiles and table picker.
	mux.HandleFunc("GET /api/sync/tables", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := syncTill(r, tills); !ok {
			writeSyncOrdersJSON(w, http.StatusUnauthorized, nil, "unauthorized")
			return
		}
		states, err := posRepo.ListTablesWithState(r.Context())
		if err != nil {
			logging.L().Errorf("sync tables list: %v", err)
			writeSyncOrdersJSON(w, http.StatusInternalServerError, nil, "server error")
			return
		}
		rows := make([]syncTableRow, 0, len(states))
		for _, s := range states {
			rows = append(rows, syncTableRow{
				ID:            s.ID,
				Label:         s.Label,
				AreaZone:      s.AreaZone,
				SeatCount:     s.SeatCount,
				Shape:         s.Shape,
				PosX:          s.PosX,
				PosY:          s.PosY,
				Enabled:       s.Enabled,
				CreatedAt:     s.CreatedAt,
				UpdatedAt:     s.UpdatedAt,
				Occupied:      s.Occupied,
				OccupiedSince: s.OccupiedSince,
			})
		}
		writeSyncOrdersJSON(w, http.StatusOK, rows, nil)
	})
}
