package pages

import (
	"encoding/json"
	"net/http"

	"github.com/universaltill/universal-till/internal/httpx"
)

// writeTenderInFlight refuses a second cashier tender while one still runs on
// the engine (ut-docs#6909). Nothing was authorised, reversed or recorded.
// HX-Reswap none keeps the basket (and the first tender's pending swap) intact;
// HX-Trigger lets the sale screen show its non-modal notice. The split modal's
// fetch shows error.message as-is.
func writeTenderInFlight(w http.ResponseWriter, r *http.Request) {
	msg := httpx.T(httpx.ResolveLocale(w, r), "pos.tender.in_flight")
	trig, _ := json.Marshal(map[string]any{"tender-in-flight": map[string]any{}})
	w.Header().Set("HX-Reswap", "none")
	w.Header().Set("HX-Trigger", string(trig))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusConflict)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"data":  nil,
		"error": map[string]string{"code": "tender_in_flight", "message": msg},
	})
}
