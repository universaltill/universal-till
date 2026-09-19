package pages

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// registerVoucherAPI mounts the voucher liability query (ut-docs#1008):
// GET /api/vouchers/{id} returns one voucher's outstanding balance, stable
// identifier and holder label in the { data, error } envelope (snake_case,
// amounts in integer minor units — data.Voucher's own tags). This is the
// acceptance criteria's "outstanding voucher liability is queryable per
// voucher"; issuing happens through /api/pos/tender's issue_vouchers field,
// redemption through a tender payment's voucher_id.
//
// Cross-till (ut-docs#1668): a voucher this till has never locally seen —
// issued at a different till — falls through to fetchVoucherFromPrimary
// (voucher_sync_proxy.go) on a REPLICA with a reachable primary, exactly
// like every other cross-till read in this codebase. Read-only: this never
// writes a local mirror row (only the redemption write-through does, right
// before it needs one to debit). Any failure reaching the primary (not a
// replica, network error, non-200, malformed body) falls back to the
// existing local 404 — offline-first, unchanged.
func registerVoucherAPI(mux *http.ServeMux, d *common.Deps) {
	repo := data.NewPOSRepo(d.Db)

	writeEnvelope := func(w http.ResponseWriter, status int, payload any, code, message string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		var errField any
		if code != "" {
			errField = map[string]string{"code": code, "message": message}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": payload, "error": errField})
	}

	mux.HandleFunc("GET /api/vouchers/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.PathValue("id"))
		if id == "" || len(id) > 64 {
			writeEnvelope(w, http.StatusBadRequest, nil, "invalid_voucher_id", "voucher id must be 1-64 characters")
			return
		}
		v, err := repo.GetVoucherBalance(r.Context(), nil, id)
		if errors.Is(err, data.ErrVoucherNotFound) {
			if primaryV, ok := fetchVoucherFromPrimary(r.Context(), d, voucherProxyClient, id); ok {
				writeEnvelope(w, http.StatusOK, primaryV, "", "")
				return
			}
			writeEnvelope(w, http.StatusNotFound, nil, "voucher_not_found", "no voucher with this identifier")
			return
		}
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "pos.error.server", "voucher-api", err)
			return
		}
		writeEnvelope(w, http.StatusOK, v, "", "")
	})

	// POST /api/vouchers/{id}/redeem (ut-docs#1037): the SINGLE-PURPOSE
	// redemption — the hand-over of the specific, already-taxed good the
	// voucher was sold for. Deliberately NOT a tender: no sale, no payment
	// row, no tax event (the VAT was declared by the issuing sale), just
	// data.RedeemSinglePurposeVoucher inside one transaction. A
	// multi-purpose voucher is refused here with its own code — it is
	// PAYMENT and goes through /api/pos/tender's voucher_id. Same
	// middleware-level auth as the GET above (this file registers no gate
	// of its own; /api/vouchers/* is a logged-in-cashier surface). Body is
	// optional JSON: {"sale_id": "..."} attaches a soft, informational sale
	// reference to the redemption row; an empty body is fine, a malformed
	// one is a 400 (validate all external input). Local-only by design —
	// unlike the balance check, this never proxies to the primary: a
	// redemption that drained only a replica's mirror while the primary
	// still showed 'active' would be a cross-till double redemption, so a
	// voucher this till has no local row for is a 404 here (the balance
	// check already told the cashier which till issued it). The cross-till
	// write-through for this kind is a follow-up, same shape as ADR-0084's
	// for multi-purpose.
	mux.HandleFunc("POST /api/vouchers/{id}/redeem", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.PathValue("id"))
		if id == "" || len(id) > 64 {
			writeEnvelope(w, http.StatusBadRequest, nil, "invalid_voucher_id", "voucher id must be 1-64 characters")
			return
		}
		var in struct {
			SaleID string `json:"sale_id"`
		}
		if r.Body != nil && r.ContentLength != 0 {
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil && !errors.Is(err, io.EOF) {
				writeEnvelope(w, http.StatusBadRequest, nil, "invalid_body", "body must be a JSON object")
				return
			}
		}
		in.SaleID = strings.TrimSpace(in.SaleID)
		if len(in.SaleID) > 64 {
			writeEnvelope(w, http.StatusBadRequest, nil, "invalid_sale_id", "sale id must be at most 64 characters")
			return
		}
		tx, err := d.Db.BeginTx(r.Context(), nil)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "pos.error.server", "voucher-api", err)
			return
		}
		defer tx.Rollback()
		v, err := repo.RedeemSinglePurposeVoucher(r.Context(), tx, id, in.SaleID, time.Now().UTC().Format(time.RFC3339))
		switch {
		case errors.Is(err, data.ErrVoucherNotFound):
			writeEnvelope(w, http.StatusNotFound, nil, "voucher_not_found", "no voucher with this identifier")
			return
		case errors.Is(err, data.ErrVoucherNotSinglePurpose):
			writeEnvelope(w, http.StatusConflict, nil, "voucher_not_single_purpose", "this voucher is redeemed as payment, not handed over")
			return
		case errors.Is(err, data.ErrVoucherNotActive):
			writeEnvelope(w, http.StatusConflict, nil, "voucher_not_active", "voucher already redeemed or no longer active")
			return
		case err != nil:
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "pos.error.server", "voucher-api", err)
			return
		}
		if err := tx.Commit(); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "pos.error.server", "voucher-api", err)
			return
		}
		writeEnvelope(w, http.StatusOK, v, "", "")
	})
}
