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
// redemption through a tender payment's voucher_id. Local SQLite only —
// never a network dependency (offline-first, ADR-0003).
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
		v, err := repo.GetVoucherBalance(r.Context(), id)
		if errors.Is(err, data.ErrVoucherNotFound) {
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
