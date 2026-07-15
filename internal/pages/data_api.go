package pages

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// registerDataAPI wires the manager-gated Data-management actions. Clearing
// test data before go-live is all-or-nothing and audited (it cannot cherry-pick
// individual sales). More operations (customer erasure, catalog cleanup) build
// on this pattern.
func registerDataAPI(mux *http.ServeMux, d *common.Deps) {
	respond := func(w http.ResponseWriter, status int, ok bool, msg string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"success": ok, "message": msg})
	}

	mux.HandleFunc("POST /api/data/reset-transactions", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			respond(w, http.StatusForbidden, false, "manager only")
			return
		}
		_ = r.ParseForm()
		if strings.TrimSpace(r.FormValue("confirm")) != "RESET" {
			respond(w, http.StatusBadRequest, false, "type RESET to confirm")
			return
		}
		n, err := data.NewPOSRepo(d.Db).ResetTransactionHistory(r.Context(), auth.UserID(r))
		if err != nil {
			respond(w, http.StatusInternalServerError, false, err.Error())
			return
		}
		// Refresh the in-memory shift/menu state is unnecessary — the basket is
		// in memory and unaffected; the next receipt number restarts from 1.
		respond(w, http.StatusOK, true, fmt.Sprintf("cleared %d sales and related records", n))
	})

	// GDPR: find a customer to erase.
	mux.HandleFunc("GET /api/data/customers", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			respond(w, http.StatusForbidden, false, "manager only")
			return
		}
		list, err := data.NewPOSRepo(d.Db).SearchCustomers(r.Context(), r.URL.Query().Get("q"), 25)
		if err != nil {
			respond(w, http.StatusInternalServerError, false, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"customers": list})
	})

	// GDPR: erase a customer's personal data (keeps their sales, anonymised).
	mux.HandleFunc("POST /api/data/customers/erase", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			respond(w, http.StatusForbidden, false, "manager only")
			return
		}
		_ = r.ParseForm()
		id := strings.TrimSpace(r.FormValue("id"))
		if id == "" {
			respond(w, http.StatusBadRequest, false, "customer id required")
			return
		}
		ok, err := data.NewPOSRepo(d.Db).EraseCustomer(r.Context(), id, auth.UserID(r))
		if err != nil {
			respond(w, http.StatusInternalServerError, false, err.Error())
			return
		}
		if !ok {
			respond(w, http.StatusNotFound, false, "customer not found")
			return
		}
		respond(w, http.StatusOK, true, "customer erased")
	})
}
