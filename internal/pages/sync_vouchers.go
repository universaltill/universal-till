package pages

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Cross-till voucher endpoints, primary side. vouchers/voucher_transactions
// are deliberately NOT part of the admin-bundle sync (sync_admin_repo.go's
// adminTables) — a periodic primary-wins dump/apply on a balance that can
// change between polls risks clobbering a redemption made on a satellite
// since the primary's last pull, the same hazard ut-docs#1554 named for
// role_permissions. Instead the primary is the one shared serialization
// point a replica talks to live, at the moment it matters:
//
//   - GET /api/sync/vouchers/{id} (ut-docs#1668): a read-only balance lookup,
//     used by voucher_api.go's plain balance query via fetchVoucherFromPrimary.
//   - POST /api/sync/vouchers/{id}/redeem and .../{id}/release (ADR-0084,
//     ut-docs#1716): an idempotent RESERVATION pair. At tender time a
//     replica's voucherRedeemWriteThrough (voucher_sync_proxy.go) reserves
//     each tracked voucher payment here BEFORE completing the sale locally;
//     /redeem runs DebitVoucherForRedemption's predicate-guarded UPDATE on
//     the PRIMARY's database, so two tills racing for the same balance
//     commit one after the other and the second loses `balance >= ?`
//     cleanly — that is the whole mechanism that closes the fully-
//     simultaneous two-till double-redemption race #1668 left open.
//
// This supersedes the round-2 review decision (2026-09-07) that kept this
// surface deliberately read-only. That decision was right about what it
// ruled out: the first draft's synchronous primary debit had NO idempotency
// mechanism, so the replica's own completed sale journaled the SAME debit up
// again minutes later (applyJournal forces it through via
// AllowVoucherOverdraft with no idea the write-through already applied it),
// and a later refused payment in the same sale orphaned an earlier
// payment's committed debit. ADR-0084 supplies the missing mechanism rather
// than re-deriving that argument here: /redeem records its debit as a
// 'redemption' voucher_transactions row keyed on the SAME sale id the
// replica's local sale and its journal replay carry, backed by the
// ux_voucher_tx_redemption_once unique index (migration 012); a retried
// /redeem for that (voucher_id, sale_id) debits nothing, and
// pos.CompleteSale checks the same key (VoucherRedemptionRecorded) before
// its own debit, so the journal replay skips a reservation it already sees.
// The orphan is closed by /release: the replica unwinds every reservation
// it made in a tender that then fails, synchronously, before any local
// sale row exists — so no journal entry for a released attempt can ever be
// produced. The residual crash window between a successful /redeem and the
// local sale committing is accepted, not solved (ADR-0084 Decision 4).
//
// The primary/offline-only fallback is unchanged: a replica that cannot
// reach this surface proceeds exactly as before #1668 (local-only
// validation, AllowVoucherOverdraft on replay) — offline-first is not
// weakened.
//
// Bearer-authed via syncTill, JSON envelope { "data": …, "error": null },
// snake_case — same conventions as every other /api/sync/* endpoint. All
// three must stay on internal/auth/middleware.go's exempt list
// (TestSyncPullPathsAreExempt pins them), or a replica is 401'd before
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

// voucherRedeemRequest is POST .../{id}/redeem's body. sale_id is the
// idempotency key (ADR-0084 Decision 2) — the replica's already-minted local
// sale id, which its journal replay will carry unchanged.
type voucherRedeemRequest struct {
	SaleID      string `json:"sale_id"`
	AmountMinor int64  `json:"amount_minor"`
}

// voucherReleaseRequest is POST .../{id}/release's body.
type voucherReleaseRequest struct {
	SaleID string `json:"sale_id"`
}

// Stable machine-readable refusal reasons carried in the envelope's `error`
// field on a 409 from /redeem, so the replica-side proxy can map each back
// to the exact data sentinel (and tell a definitive refusal apart from a
// generic failure it should fall back on). Nothing else on /api/sync/* uses
// 409 today; a refusal is a business-rule conflict with the primary's live
// state, not a caller bug (400) and not a server fault (500).
const (
	syncVoucherErrNotActive           = "voucher_not_active"
	syncVoucherErrInsufficientBalance = "voucher_insufficient_balance"
	syncVoucherErrAmountMismatch      = "voucher_redemption_amount_mismatch"
)

// registerSyncVouchers mounts the primary-side voucher endpoints on the
// bearer-authed /api/sync/* surface.
func registerSyncVouchers(mux *http.ServeMux, d *common.Deps) {
	tills := data.NewTillsRepo(d.Db)
	repo := data.NewPOSRepo(d.Db)

	// The primary's live voucher balance — read-only, for voucher_api.go's
	// plain balance lookup (fetchVoucherFromPrimary). The redemption path
	// does NOT go through here: it reserves via /redeem below.
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

	// POST /api/sync/vouchers/{id}/redeem — the atomic, idempotent
	// reservation (ADR-0084 Decision 1/2). One transaction owns both the
	// idempotency pre-check and the guarded debit, so under this database's
	// BEGIN IMMEDIATE two concurrent reservations serialize here. Answers
	// with the PRE-debit snapshot (the row as it was immediately before this
	// call's debit — brief addendum, correction 5): the replica seeds its
	// local mirror from it and then applies its own forced local debit, so
	// handing it the post-debit number would double-count the reduction.
	mux.HandleFunc("POST /api/sync/vouchers/{id}/redeem", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := syncTill(r, tills); !ok {
			writeSyncOrdersJSON(w, http.StatusUnauthorized, nil, "unauthorized")
			return
		}
		id := strings.TrimSpace(r.PathValue("id"))
		var in voucherRedeemRequest
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeSyncOrdersJSON(w, http.StatusBadRequest, nil, "invalid body")
			return
		}
		in.SaleID = strings.TrimSpace(in.SaleID)
		if id == "" || in.SaleID == "" {
			writeSyncOrdersJSON(w, http.StatusBadRequest, nil, "id and sale_id required")
			return
		}
		if in.AmountMinor <= 0 {
			writeSyncOrdersJSON(w, http.StatusBadRequest, nil, "amount_minor must be > 0")
			return
		}
		tx, err := d.Db.BeginTx(r.Context(), nil)
		if err != nil {
			logging.L().Errorf("sync voucher redeem %s: begin: %v", id, err)
			writeSyncOrdersJSON(w, http.StatusInternalServerError, nil, "server error")
			return
		}
		defer tx.Rollback()
		v, err := repo.ReserveVoucherRedemption(r.Context(), tx, id, in.SaleID, in.AmountMinor, time.Now().UTC().Format(time.RFC3339))
		switch {
		case errors.Is(err, data.ErrVoucherNotFound):
			writeSyncOrdersJSON(w, http.StatusNotFound, nil, "not found")
			return
		case errors.Is(err, data.ErrVoucherNotActive):
			writeSyncOrdersJSON(w, http.StatusConflict, nil, syncVoucherErrNotActive)
			return
		case errors.Is(err, data.ErrVoucherInsufficientBalance):
			writeSyncOrdersJSON(w, http.StatusConflict, nil, syncVoucherErrInsufficientBalance)
			return
		case errors.Is(err, data.ErrVoucherRedemptionAmountMismatch):
			logging.L().Errorf("sync voucher redeem %s for sale %s: %v", id, in.SaleID, err)
			writeSyncOrdersJSON(w, http.StatusConflict, nil, syncVoucherErrAmountMismatch)
			return
		case err != nil:
			logging.L().Errorf("sync voucher redeem %s for sale %s: %v", id, in.SaleID, err)
			writeSyncOrdersJSON(w, http.StatusInternalServerError, nil, "server error")
			return
		}
		if err := tx.Commit(); err != nil {
			logging.L().Errorf("sync voucher redeem %s for sale %s: commit: %v", id, in.SaleID, err)
			writeSyncOrdersJSON(w, http.StatusInternalServerError, nil, "server error")
			return
		}
		writeSyncOrdersJSON(w, http.StatusOK, voucherToSyncRow(v), nil)
	})

	// POST /api/sync/vouchers/{id}/release — /redeem's inverse (ADR-0084
	// Decision 3). Idempotent-success by design: no matching reservation
	// (already released, or never granted because /redeem refused it) is a
	// 200 no-op, never an error, so the replica can fire it for every
	// voucher a failed tender touched without first working out which ones
	// actually reserved. Only a real DB fault is a 500.
	//
	// Deliberately does NOT check that the calling till is the one that made
	// the reservation (independent review finding) — any enrolled till with
	// a valid sync bearer can release any (voucher_id, sale_id)'s
	// reservation given the sale id. Consistent with the trust model
	// syncTill already establishes for every other /api/sync/* endpoint (a
	// bearer-authed till can already journal an arbitrary sale via
	// /api/sync/sales), not a new gap this endpoint introduces.
	mux.HandleFunc("POST /api/sync/vouchers/{id}/release", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := syncTill(r, tills); !ok {
			writeSyncOrdersJSON(w, http.StatusUnauthorized, nil, "unauthorized")
			return
		}
		id := strings.TrimSpace(r.PathValue("id"))
		var in voucherReleaseRequest
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeSyncOrdersJSON(w, http.StatusBadRequest, nil, "invalid body")
			return
		}
		in.SaleID = strings.TrimSpace(in.SaleID)
		if id == "" || in.SaleID == "" {
			writeSyncOrdersJSON(w, http.StatusBadRequest, nil, "id and sale_id required")
			return
		}
		tx, err := d.Db.BeginTx(r.Context(), nil)
		if err != nil {
			logging.L().Errorf("sync voucher release %s: begin: %v", id, err)
			writeSyncOrdersJSON(w, http.StatusInternalServerError, nil, "server error")
			return
		}
		defer tx.Rollback()
		if err := repo.ReleaseVoucherRedemption(r.Context(), tx, id, in.SaleID); err != nil {
			logging.L().Errorf("sync voucher release %s for sale %s: %v", id, in.SaleID, err)
			writeSyncOrdersJSON(w, http.StatusInternalServerError, nil, "server error")
			return
		}
		if err := tx.Commit(); err != nil {
			logging.L().Errorf("sync voucher release %s for sale %s: commit: %v", id, in.SaleID, err)
			writeSyncOrdersJSON(w, http.StatusInternalServerError, nil, "server error")
			return
		}
		writeSyncOrdersJSON(w, http.StatusOK, map[string]any{"released": true}, nil)
	})
}
