package pages

import (
	"encoding/json"
	"net/http"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
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

// basketEdit runs h as one cashier basket mutation (ut-docs#6919): refused
// with writeTenderInFlight's 409 before h touches the engine or DB while a
// tender runs, and a tender that starts meanwhile waits for h to finish
// before it reads the basket. d.Engine is resolved per request (bare test
// Deps).
func basketEdit(d *common.Deps, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !d.Engine.BeginBasketEdit() {
			writeTenderInFlight(w, r)
			return
		}
		defer d.Engine.EndBasketEdit()
		h(w, r)
	}
}
