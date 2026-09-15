package pages

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Cross-till held-sale (parked order) write-through, primary side (ADR-0093,
// ut-docs#1920). held_sales is deliberately NOT part of the admin-bundle
// sync (sync_admin_repo.go's nonAdminTables): a primary-wins dump with
// delete-missing pruning would erase a satellite's own genuinely-parked
// orders on every pull — the same reason vouchers (ADR-0084) and
// table_claims (ut-docs#1703) each got a live, on-demand surface here
// instead. Until this ADR a parked order lived on exactly one till, so a
// second till or a waiter's tablet could see the table it occupied
// (ut-docs#1704's claim write-through) but never the order itself — which
// defeats "open orders" for that shop shape. Now a replica's
// heldSaleWriteThrough / heldSaleDeleteWriteThrough / fetchHeldSalesFromPrimary
// (held_sale_sync_proxy.go) talk to the PRIMARY's held_sales live, at the
// moment it matters, and the primary's one SQLite writer is the shop-wide
// serialization point:
//
//   - POST /api/sync/held-sales/upsert — idempotent create/update of one
//     parked order, guarded by HeldSalesRepo.UpsertIfNewer's updated_at
//     predicate so a stale concurrent write from a second till loses
//     cleanly instead of clobbering a newer one (last-writer-wins with
//     clean refusal — ADR-0093 Decision 2; a per-line merge is an explicit
//     non-goal there). A refusal is a 200 carrying applied=false plus the
//     CURRENT row, never a 409: like /api/sync/tables/claim's claimed=false,
//     losing the race is an ordinary outcome the caller reconciles from,
//     not an error — and unlike ADR-0084's voucher refusals there is
//     nothing for the caller to abort, only a newer version to adopt. The
//     applied=true answer carries the row too, so the replica mirrors the
//     PRIMARY's stamp rather than its own clock (see UpsertIfNewer's doc on
//     why all stamps that pass through here come from one clock).
//   - POST /api/sync/held-sales/delete — resume or abandon on any till
//     removes the order shop-wide. Idempotent-success: an already-gone id
//     is a 200 no-op, same stance as /api/sync/vouchers/{id}/release.
//   - GET /api/sync/held-sales — every currently-open parked order, for a
//     replica's open-orders page to merge against its own local rows
//     (mergeHeldSalesWithPrimary).
//
// Bearer-authed via syncTill, JSON envelope { "data": …, "error": null },
// snake_case — same conventions as every other /api/sync/* endpoint. All
// three must stay on internal/auth/middleware.go's exempt list
// (TestSyncPullPathsAreExempt pins them), or a replica is 401'd before
// syncTill ever runs and the proxy silently falls back to local-only — the
// /api/sync/stock incident that comment documents.

// syncHeldSaleRow is the wire form of one data.HeldSale. payload travels as
// the opaque JSON string the row stores — the primary never decodes it; it
// is the replica that parked the order, and the replica that resumes it,
// who own its meaning.
type syncHeldSaleRow struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	TotalMinor int64  `json:"total_minor"`
	LineCount  int    `json:"line_count"`
	Payload    string `json:"payload"`
	TableID    string `json:"table_id"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

func heldSaleToSyncRow(h data.HeldSale) syncHeldSaleRow {
	return syncHeldSaleRow{
		ID:         h.ID,
		Label:      h.Label,
		TotalMinor: h.TotalMinor,
		LineCount:  h.LineCount,
		Payload:    h.Payload,
		TableID:    h.TableID,
		CreatedAt:  h.CreatedAt,
		UpdatedAt:  h.UpdatedAt,
	}
}

func heldSaleFromSyncRow(r syncHeldSaleRow) data.HeldSale {
	return data.HeldSale{
		ID:         r.ID,
		Label:      r.Label,
		TotalMinor: r.TotalMinor,
		LineCount:  r.LineCount,
		Payload:    r.Payload,
		TableID:    r.TableID,
		CreatedAt:  r.CreatedAt,
		UpdatedAt:  r.UpdatedAt,
	}
}

// syncHeldSaleUpsertResult is POST .../upsert's answer. row is the row as it
// stands on the primary AFTER the call — the caller's own write when
// applied, the newer one that beat it when not — so one read-back shape
// serves both outcomes. nil only if the row vanished between the write and
// the read-back (a concurrent /delete), which the replica treats as
// "nothing to mirror".
type syncHeldSaleUpsertResult struct {
	Applied bool             `json:"applied"`
	Row     *syncHeldSaleRow `json:"row"`
}

// syncHeldSaleDeleteRequest is POST .../delete's body.
type syncHeldSaleDeleteRequest struct {
	ID string `json:"id"`
}

// syncHeldSaleDeleteResult is POST .../delete's answer — always deleted=true
// on a 200: delete is idempotent, so there is no failure to report beyond
// auth/validation.
type syncHeldSaleDeleteResult struct {
	Deleted bool `json:"deleted"`
}

// registerSyncHeldSales mounts the primary-side held-sale endpoints on the
// bearer-authed /api/sync/* surface.
func registerSyncHeldSales(mux *http.ServeMux, d *common.Deps) {
	tills := data.NewTillsRepo(d.Db)
	repo := data.NewHeldSalesRepo(d.Db)

	// The primary's live parked orders — the same rows HeldSalesRepo.List
	// feeds the local open-orders page and strip.
	mux.HandleFunc("GET /api/sync/held-sales", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := syncTill(r, tills); !ok {
			writeSyncOrdersJSON(w, http.StatusUnauthorized, nil, "unauthorized")
			return
		}
		items, err := repo.List(r.Context())
		if err != nil {
			logging.L().Errorf("sync held sales list: %v", err)
			writeSyncOrdersJSON(w, http.StatusInternalServerError, nil, "server error")
			return
		}
		rows := make([]syncHeldSaleRow, 0, len(items))
		for _, h := range items {
			rows = append(rows, heldSaleToSyncRow(h))
		}
		writeSyncOrdersJSON(w, http.StatusOK, rows, nil)
	})

	// Guarded upsert of one parked order (ADR-0093 Decision 2). 200 with
	// applied=false is the ordinary "a newer version already exists"
	// answer, never an error status — the replica adopts the row carried
	// back. An empty updated_at is stamped on THIS clock by UpsertIfNewer,
	// which is what every replica's own fresh write sends.
	mux.HandleFunc("POST /api/sync/held-sales/upsert", func(w http.ResponseWriter, r *http.Request) {
		till, ok := syncTill(r, tills)
		if !ok {
			writeSyncOrdersJSON(w, http.StatusUnauthorized, nil, "unauthorized")
			return
		}
		var in syncHeldSaleRow
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeSyncOrdersJSON(w, http.StatusBadRequest, nil, "invalid body")
			return
		}
		in.ID = strings.TrimSpace(in.ID)
		if in.ID == "" {
			writeSyncOrdersJSON(w, http.StatusBadRequest, nil, "id required")
			return
		}
		applied, err := repo.UpsertIfNewer(r.Context(), heldSaleFromSyncRow(in))
		if err != nil {
			logging.L().Errorf("sync held sale upsert %s from %s: %v", in.ID, till.Name, err)
			writeSyncOrdersJSON(w, http.StatusInternalServerError, nil, "server error")
			return
		}
		cur, found, err := repo.Get(r.Context(), in.ID)
		if err != nil {
			logging.L().Errorf("sync held sale upsert %s from %s: read back: %v", in.ID, till.Name, err)
			writeSyncOrdersJSON(w, http.StatusInternalServerError, nil, "server error")
			return
		}
		if !applied {
			logging.L().Debugf("sync held sale upsert %s from %s: refused, a newer version is already on this primary", in.ID, till.Name)
		}
		out := syncHeldSaleUpsertResult{Applied: applied}
		if found {
			row := heldSaleToSyncRow(cur)
			out.Row = &row
		}
		writeSyncOrdersJSON(w, http.StatusOK, out, nil)
	})

	// Remove one parked order shop-wide. Idempotent — deleting an id that is
	// already gone is still 200/deleted, so a replica can fire it on every
	// resume without first asking whether the primary ever had the row.
	mux.HandleFunc("POST /api/sync/held-sales/delete", func(w http.ResponseWriter, r *http.Request) {
		till, ok := syncTill(r, tills)
		if !ok {
			writeSyncOrdersJSON(w, http.StatusUnauthorized, nil, "unauthorized")
			return
		}
		var in syncHeldSaleDeleteRequest
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeSyncOrdersJSON(w, http.StatusBadRequest, nil, "invalid body")
			return
		}
		in.ID = strings.TrimSpace(in.ID)
		if in.ID == "" {
			writeSyncOrdersJSON(w, http.StatusBadRequest, nil, "id required")
			return
		}
		if err := repo.Delete(r.Context(), in.ID); err != nil {
			logging.L().Errorf("sync held sale delete %s from %s: %v", in.ID, till.Name, err)
			writeSyncOrdersJSON(w, http.StatusInternalServerError, nil, "server error")
			return
		}
		writeSyncOrdersJSON(w, http.StatusOK, syncHeldSaleDeleteResult{Deleted: true}, nil)
	})
}
