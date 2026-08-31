package pages

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Cross-till orders (ut-docs#1350): the primary-side sync surface for the
// order-status board. sales/order_status are deliberately NOT part of the
// admin-bundle sync (sync_admin_repo.go) and the sales journal only pushes a
// sale at CREATION (sync_sales.go) — a later status tap never re-syncs. So
// instead of replicating state, a replica's /ui/orders and one-tap POST proxy
// to these two endpoints (order_status.go's fetchOrdersFromPrimary /
// applyOrderStatusOnPrimary), making the PRIMARY's DB the single live board
// every till reads and writes while reachable. Same shape as
// registerSyncSales: bearer-authed via syncTill, JSON envelope
// { "data": …, "error": null }, snake_case.

// syncOrderRow is the wire form of one data.OrderListEntry — the same rows
// the local /ui/orders fragment renders, so a replica can re-render its own
// HTML fragment from them unchanged.
type syncOrderRow struct {
	ReceiptNo            string `json:"receipt_no"`
	OrderType            string `json:"order_type"`
	Status               string `json:"status"`
	StatusUpdatedAt      string `json:"status_updated_at"`
	CreatedAt            string `json:"created_at"`
	KitchenPrintFailedAt string `json:"kitchen_print_failed_at"`
	ReceiptPrintFailedAt string `json:"receipt_print_failed_at"`
}

// syncOrderStatusResult is the wire form of one guarded status write's
// outcome (orderStatusOutcome, minus the transport-level BadStatus/Found
// which map to 400/404) — structured data, not an HTML fragment: the replica
// re-renders its own local fragment (writeOrderStatusFragment) in its own
// locale from this.
type syncOrderStatusResult struct {
	Applied bool   `json:"applied"`
	Tracked bool   `json:"tracked"`
	Status  string `json:"status"`
	Who     string `json:"who"`
	When    string `json:"when"`
}

func writeSyncOrdersJSON(w http.ResponseWriter, status int, data, errMsg any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "error": errMsg})
}

// registerSyncOrders mounts the primary-side order endpoints on the
// bearer-authed /api/sync/* surface.
func registerSyncOrders(mux *http.ServeMux, d *common.Deps) {
	tills := data.NewTillsRepo(d.Db)

	// The primary's live recent-orders board — the same rows
	// ListRecentOrders feeds the local fragment.
	mux.HandleFunc("GET /api/sync/orders", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := syncTill(r, tills); !ok {
			writeSyncOrdersJSON(w, http.StatusUnauthorized, nil, "unauthorized")
			return
		}
		entries, err := data.NewPOSRepo(d.Db).ListRecentOrders(r.Context(), 50)
		if err != nil {
			logging.L().Errorf("sync orders list: %v", err)
			writeSyncOrdersJSON(w, http.StatusInternalServerError, nil, "server error")
			return
		}
		rows := make([]syncOrderRow, 0, len(entries))
		for _, e := range entries {
			rows = append(rows, syncOrderRow{
				ReceiptNo:            e.ReceiptNo,
				OrderType:            e.OrderType,
				Status:               e.Status,
				StatusUpdatedAt:      e.StatusUpdatedAt,
				CreatedAt:            e.CreatedAt,
				KitchenPrintFailedAt: e.KitchenPrintFailedAt,
				ReceiptPrintFailedAt: e.ReceiptPrintFailedAt,
			})
		}
		writeSyncOrdersJSON(w, http.StatusOK, rows, nil)
	})

	authRepo := data.NewAuthRepo(d.Db)

	// The SAME guarded write as the human-facing one-tap endpoint —
	// applyOrderStatusCore is shared, never duplicated. actor_id (the
	// REPLICA's own session user, ut-docs#1350 review) is honored as the
	// journal/audit actor only once it resolves to a REAL ROW in this
	// (primary) till's own users table — existence-checked, not further
	// authenticated: a bearer-authed peer already has journal-push/snapshot-
	// read trust (it's an enrolled till, not an anonymous caller), so this
	// only stops an arbitrary UNVALIDATED string reaching audit_log's
	// users-FK column, not a claim of any *existing* operator's id — a
	// stronger check (e.g. requiring that operator to hold a live session
	// somewhere) would need this endpoint to carry more than a till's own
	// bearer, which it deliberately doesn't (round 2 review: judged
	// acceptable — the forgeable actions are low-stakes status taps, never
	// money/fiscal, and source_till always rides the audit payload
	// regardless, so an investigator can always see which till relayed it).
	// No resolvable operator (older replica, no session, or an id this
	// till's users table doesn't have) falls back to the calling TILL as
	// the journal actor, same attribution convention as applyJournal's
	// till-attributed writes. Status codes mirror the human-facing
	// endpoint: 400 bad status, 404 unknown receipt, 200 for both an
	// applied and a silently-dropped stale write.
	mux.HandleFunc("POST /api/sync/orders/{receipt_no}/status", func(w http.ResponseWriter, r *http.Request) {
		till, ok := syncTill(r, tills)
		if !ok {
			writeSyncOrdersJSON(w, http.StatusUnauthorized, nil, "unauthorized")
			return
		}
		_ = r.ParseForm()
		receiptNo := strings.TrimSpace(r.PathValue("receipt_no"))
		next := strings.TrimSpace(r.Form.Get("status"))

		actorID := till.Name
		if actorID == "" {
			actorID = till.ID
		}
		auditActorID := "system" // audit_log.actor_id has a users-FK a till name/id would violate.
		if claimed := strings.TrimSpace(r.Form.Get("actor_id")); claimed != "" {
			if u, found, err := authRepo.GetUser(r.Context(), claimed); err == nil && found {
				actorID = u.ID
				auditActorID = u.ID
			}
		}
		// source_till rides the audit payload regardless of whether the
		// operator resolved — which till relayed the tap is worth keeping
		// even when who tapped it is also known.
		sourceTill := till.Name
		if sourceTill == "" {
			sourceTill = till.ID
		}
		res, err := applyOrderStatusCore(r.Context(), d, receiptNo, next, actorID, auditActorID, sourceTill)
		switch {
		case err != nil:
			logging.L().Errorf("sync order status %s from %s: %v", receiptNo, till.Name, err)
			writeSyncOrdersJSON(w, http.StatusInternalServerError, nil, "server error")
		case res.BadStatus:
			writeSyncOrdersJSON(w, http.StatusBadRequest, nil, "invalid status")
		case !res.Found:
			writeSyncOrdersJSON(w, http.StatusNotFound, nil, "not found")
		default:
			writeSyncOrdersJSON(w, http.StatusOK, syncOrderStatusResult{
				Applied: res.Applied,
				Tracked: res.Tracked,
				Status:  res.Status,
				Who:     res.Who,
				When:    res.When,
			}, nil)
		}
	})
}
