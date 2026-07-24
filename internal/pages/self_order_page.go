package pages

import (
	"net/http"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// registerSelfOrder serves the self-order kiosk flow (ADR-0020, spec 011
// Phase 2 — shell only; browse/search/customize/cart is Phase 3, checkout
// is Phase 4). GET /self-order and everything under it is auth-exempt
// (internal/auth/middleware.go) — used by anonymous walk-up customers who
// cannot PIN-login. Reachable regardless of the current display.mode
// setting (same "any till can visit it directly" precedent as
// /backoffice) — the mode only controls whether "/" redirects here for an
// already-authenticated visitor.
func registerSelfOrder(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("GET /self-order", func(w http.ResponseWriter, r *http.Request) {
		st := d.CurrentState()
		httpx.RenderPartial("ui/pages/self_order.html", map[string]any{
			"title":         "Self-order",
			"idleResetSecs": st.KioskIdleResetSeconds,
			"shopName":      d.Cfg.StoreName,
		})(w, r)
	})
}
