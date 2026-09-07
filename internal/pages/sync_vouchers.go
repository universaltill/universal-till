package pages

import (
	"errors"
	"net/http"
	"strings"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Cross-till voucher lookup, primary side (ut-docs#1668): vouchers/
// voucher_transactions are deliberately NOT part of the admin-bundle sync
// (sync_admin_repo.go's adminTables) — a periodic primary-wins dump/apply on
// a balance that can change between polls risks clobbering a redemption made
// on a satellite since the primary's last pull, the same hazard ut-docs#1554
// named for role_permissions. Instead: a replica's fetchVoucherFromPrimary /
// voucherRedeemWriteThrough (voucher_sync_proxy.go) read this endpoint to
// validate a redemption against the primary's CURRENT balance right before
// completing the sale LOCALLY — the actual debit still happens exactly once,
// locally, and reaches the primary the same way it always has: the ordinary
// sales journal (sync_sales.go's applyJournal), completely unchanged.
//
// Deliberately READ-ONLY, no redeem/debit endpoint here (round-2 review,
// 2026-09-07). The first draft of this card added a
// POST /api/sync/vouchers/{id}/redeem that ALSO debited the primary
// synchronously, in addition to the debit the replica's own completed sale
// journals up moments later via the existing, unconditional per-sale sync —
// double-debiting every online cross-till redemption on the happy path (the
// journal replay has no idea the write-through already applied it, and
// forces the debit through regardless via AllowVoucherOverdraft). A second,
// aborted, still-committed primary debit ALSO orphaned itself whenever a
// later payment in the same sale was refused (ut-docs#1668 review, blockers
// 1 and 2) — nothing on this path commits primary-side state at all now, so
// there is nothing to orphan or double-apply. The real fix for a genuinely
// atomic, race-free cross-till serialization (the journal replay explicitly
// skipping a redemption the write-through already applied, needing a shared
// idempotency key) is real future work, same shape as ut-docs#1703 splitting
// write-through claim enforcement out of ut-docs#1392's read-only slice —
// tracked as a follow-up, not solved here. What THIS card delivers: a
// replica can look up AND validate a voucher it has never locally seen
// against a FRESH, moments-old primary balance, instead of failing closed
// forever with ErrVoucherNotFound — a real, large improvement even though
// the fully-simultaneous two-till race isn't eliminated (same residual risk
// class this codebase already accepts for the single-till-offline case,
// AllowVoucherOverdraft/ut-docs#1053).
//
// Bearer-authed via syncTill, JSON envelope { "data": …, "error": null },
// snake_case — same conventions as every other /api/sync/* endpoint. Must
// stay on internal/auth/middleware.go's exempt list
// (TestSyncPullPathsAreExempt pins it), or a replica is 401'd before
// syncTill ever runs and the proxy silently falls back to local-only — the
// /api/sync/stock incident that comment documents.

// syncVoucherRow is the wire form of one data.Voucher.
type syncVoucherRow struct {
	ID             string `json:"id"`
	HolderLabel    string `json:"holder_label"`
	OriginalAmount int64  `json:"original_amount"`
	Balance        int64  `json:"balance"`
	Currency       string `json:"currency"`
	VoucherType    string `json:"voucher_type"`
	Status         string `json:"status"`
	IssuedSaleID   string `json:"issued_sale_id"`
	CreatedAt      string `json:"created_at"`
}

func voucherToSyncRow(v data.Voucher) syncVoucherRow {
	return syncVoucherRow{
		ID:             v.ID,
		HolderLabel:    v.HolderLabel,
		OriginalAmount: v.OriginalAmountMinor,
		Balance:        v.BalanceMinor,
		Currency:       v.Currency,
		VoucherType:    v.VoucherType,
		Status:         v.Status,
		IssuedSaleID:   v.IssuedSaleID,
		CreatedAt:      v.CreatedAt,
	}
}

// registerSyncVouchers mounts the primary-side voucher lookup on the
// bearer-authed /api/sync/* surface.
func registerSyncVouchers(mux *http.ServeMux, d *common.Deps) {
	tills := data.NewTillsRepo(d.Db)
	repo := data.NewPOSRepo(d.Db)

	// The primary's live voucher balance — read-only. A replica falls
	// through to this both for a plain balance lookup (voucher_api.go) and
	// as the pre-debit validation check before a local redemption
	// (voucher_sync_proxy.go's voucherRedeemWriteThrough) — same endpoint,
	// same trust boundary, no separate write path.
	mux.HandleFunc("GET /api/sync/vouchers/{id}", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := syncTill(r, tills); !ok {
			writeSyncOrdersJSON(w, http.StatusUnauthorized, nil, "unauthorized")
			return
		}
		id := strings.TrimSpace(r.PathValue("id"))
		if id == "" {
			writeSyncOrdersJSON(w, http.StatusBadRequest, nil, "id required")
			return
		}
		v, err := repo.GetVoucherBalance(r.Context(), nil, id)
		if errors.Is(err, data.ErrVoucherNotFound) {
			writeSyncOrdersJSON(w, http.StatusNotFound, nil, "not found")
			return
		}
		if err != nil {
			logging.L().Errorf("sync voucher lookup %s: %v", id, err)
			writeSyncOrdersJSON(w, http.StatusInternalServerError, nil, "server error")
			return
		}
		writeSyncOrdersJSON(w, http.StatusOK, voucherToSyncRow(v), nil)
	})
}
