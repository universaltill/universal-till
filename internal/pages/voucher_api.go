package pages

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

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
}
