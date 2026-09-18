package pages

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Cross-till held-sale (open order) sync, primary side (ADR-0093,
// ut-docs#1920). held_sales is deliberately NOT part of the admin-bundle
// sync (sync_admin_repo.go's nonAdminTables: a deleteMissing-pruning bundle
// would erase a satellite's own genuinely-parked order on every pull), so
// before this card an order's CONTENTS never crossed tills at all -- only
// the table occupancy it implied (table_claims' own write-through,
// ut-docs#1703/#1704). Now the PRIMARY's held_sales is the shop-wide copy a
// replica writes through to at the moment it matters, exactly the way
// sync_tables_claim.go does for a claim, and reads back from for its Open
// orders page:
//
//   - POST /api/sync/held-sales/upsert -- the full row. A blank incoming
//     updated_at is stamped with THIS (primary) process's own clock before
//     the guard runs (ut-docs#2271), never the replica's -- the primary is
//     the single serialization point below, so it is also the one clock
//     the guard should ever measure two tills' writes against; the applied
//     value is handed back on the wire (syncHeldSaleUpsertResult.UpdatedAt)
//     so the replica can mirror the exact value locally. Runs
//     HeldSalesRepo.UpsertIfNewer, a predicate-guarded upsert (`WHERE
//     held_sales.updated_at <= excluded.updated_at`), so two tills pushing
//     an update to the SAME order close together serialize on the primary's
//     single-writer database and whichever carries the OLDER updated_at is
//     answered applied=false on a 200 -- a clean, detectable refusal, never
//     a silent clobber of a newer edit. Same depth ADR-0084 shipped for a
//     voucher balance (sync_vouchers.go's /redeem), not a per-line merge
//     (explicit ADR-0093 non-goal).
//   - POST /api/sync/held-sales/delete -- {id}, for resume-to-active. A
//     plain delete: naturally idempotent, so an already-gone row is still
//     200/deleted=true, same as /api/sync/tables/release.
//   - GET /api/sync/held-sales -- every currently-open held sale on the
//     primary, for a replica's open_orders_page.go merge.
//
// Bearer-authed via syncTill, JSON envelope { "data": …, "error": null },
// snake_case -- same conventions as every other /api/sync/* endpoint.
// JSON bodies (not form-encoded like the claim endpoints) because the row
// carries the basket snapshot as a JSON payload string. All three must
// stay on internal/auth/middleware.go's exempt list
// (TestSyncPullPathsAreExempt pins them), or a replica is 401'd before
// syncTill ever runs and the proxy silently falls back to local-only --
// the /api/sync/stock failure class that comment documents.

// syncHeldSaleRow is the wire form of one data.HeldSale. created_at rides
// along so a re-park after a cross-till resume (the primary's row was
// deleted by the resume, this recreates it) keeps the ORIGINAL first-parked
// time the Open orders page shows as the order's age (ut-docs#1918) --
// honoured on insert, left alone on update, exactly as the repo's own
// Upsert does. updated_at is the predicate the guard compares.
type syncHeldSaleRow struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	TableID    string `json:"table_id"`
	Payload    string `json:"payload"`
	LineCount  int    `json:"line_count"`
	TotalMinor int64  `json:"total_minor"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

func heldSaleToSyncRow(h data.HeldSale) syncHeldSaleRow {
	return syncHeldSaleRow{
		ID:         h.ID,
		Label:      h.Label,
		TableID:    h.TableID,
		Payload:    h.Payload,
		LineCount:  h.LineCount,
		TotalMinor: h.TotalMinor,
		CreatedAt:  h.CreatedAt,
		UpdatedAt:  h.UpdatedAt,
	}
}

func heldSaleFromSyncRow(row syncHeldSaleRow) data.HeldSale {
	return data.HeldSale{
		ID:         row.ID,
		Label:      row.Label,
		TableID:    row.TableID,
		Payload:    row.Payload,
		LineCount:  row.LineCount,
		TotalMinor: row.TotalMinor,
		CreatedAt:  row.CreatedAt,
		UpdatedAt:  row.UpdatedAt,
	}
}

// syncHeldSaleUpsertResult is the wire form of an upsert outcome.
// applied=false on a 200 is the predicate refusal: the primary already
// holds a NEWER write for this id. UpdatedAt (ut-docs#2271) is the value
// the primary actually stored -- either the caller's own, when it sent
// one, or the primary's own clock, when it left it blank -- so the caller
// can mirror the row locally under the exact value the primary holds
// rather than re-deriving one from its own, possibly skewed, clock.
// CreatedAt (ut-docs#2394) is the same fix applied to the FIRST-park clock
// read: on a genuine first park the caller sends it blank, and this is the
// primary's own clock stamp for it, so the caller's local mirror lands the
// exact same value instead of an independent, possibly-skewed clock read
// of its own landing a second or two apart (ut-docs#2389's flake). On an
// update the value is simply echoed back unchanged -- created_at is never
// touched by the update branch (ut-docs#1918), so it carries no new
// information there, same as it carries none for a re-park's own
// caller-supplied HeldOrigin.CreatedAt.
type syncHeldSaleUpsertResult struct {
	Applied   bool   `json:"applied"`
	UpdatedAt string `json:"updated_at"`
	CreatedAt string `json:"created_at"`
}

// syncHeldSaleDeleteRequest is POST .../delete's body.
type syncHeldSaleDeleteRequest struct {
	ID string `json:"id"`
}

// syncHeldSaleDeleteResult is the wire form of a delete outcome -- always
// deleted=true on a 200: delete is idempotent, so there is no failure to
// report beyond auth/validation (same as syncTableReleaseResult).
type syncHeldSaleDeleteResult struct {
	Deleted bool `json:"deleted"`
}

// registerSyncHeldSales mounts the primary-side held-sale endpoints on the
// bearer-authed /api/sync/* surface, next to registerSyncTablesClaim's.
func registerSyncHeldSales(mux *http.ServeMux, d *common.Deps) {
	tills := data.NewTillsRepo(d.Db)
	repo := data.NewHeldSalesRepo(d.Db)

	// Upsert the full row, guarded on updated_at. 200 with applied=false is
	// the ordinary "a newer write already landed" answer -- a business
	// refusal against the primary's live state, never an error status.
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
		if in.ID == "" || in.Payload == "" {
			writeSyncOrdersJSON(w, http.StatusBadRequest, nil, "id and payload required")
			return
		}
		// ut-docs#2271: a blank incoming updated_at is stamped with THIS
		// process's own clock -- the primary is the single serialization
		// point for this table already (that's what makes the guard below
		// safe), so it is also the one clock two tills' predicate comparison
		// should ever be measured against, closing the clock-skew residual
		// ADR-0093 Decision 2 accepted. A caller-supplied value (a direct/
		// test caller, or a future non-blank use) is still honoured as-is.
		in.UpdatedAt = strings.TrimSpace(in.UpdatedAt)
		if in.UpdatedAt == "" {
			in.UpdatedAt = time.Now().UTC().Format(heldSaleTimeLayout)
		}
		// ut-docs#2394: same fix as #2271 above, for created_at -- a blank
		// incoming value (a genuine first park) is stamped with THIS
		// primary's own clock before the insert, rather than left for the
		// replica's own, independent clock read on its local mirror to
		// (mis)match by up to a second or two. A non-blank value (a
		// re-park's own HeldOrigin.CreatedAt) is honoured as-is, same as
		// UpdatedAt above.
		in.CreatedAt = strings.TrimSpace(in.CreatedAt)
		if in.CreatedAt == "" {
			in.CreatedAt = time.Now().UTC().Format(heldSaleTimeLayout)
		}
		applied, err := repo.UpsertIfNewer(r.Context(), heldSaleFromSyncRow(in))
		if err != nil {
			logging.L().Errorf("sync held sale upsert %s from %s: %v", in.ID, till.Name, err)
			writeSyncOrdersJSON(w, http.StatusInternalServerError, nil, "server error")
			return
		}
		if !applied {
			logging.L().Debugf("sync held sale upsert %s from %s: refused, a newer write already holds the row (ADR-0093)", in.ID, till.Name)
		}
		writeSyncOrdersJSON(w, http.StatusOK, syncHeldSaleUpsertResult{Applied: applied, UpdatedAt: in.UpdatedAt, CreatedAt: in.CreatedAt}, nil)
	})

	// Delete by id. Idempotent -- a delete with nothing to delete is still
	// 200/deleted.
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

	// The primary's live open orders -- the same rows its own Open orders
	// page and held strip read.
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
}
