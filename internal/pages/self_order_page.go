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
		// Landing here always means "start fresh" — whether an operator
		// navigated here directly, or the shop-page idle timer (Phase 3)
		// redirected back after inactivity. Without this, an abandoned
		// cart would silently greet the NEXT customer instead of an empty
		// one, since the basket is till-process-level state, not
		// per-visit (see spec 011 Phase 2's "revisit once there's real
		// cart state to discard on reset" note — this is that revisit).
		// KioskEngine, not Engine (ut-docs#449): this route is reachable by
		// any LAN client in any display mode, so resetting the shared
		// cashier engine here used to wipe the till's live sale. Only the
		// kiosk's own basket may be cleared.
		// d.KioskEngine is nil in some page-level test harnesses that never
		// exercise the basket (e.g. TestSelfOrderModeRedirectsHome) — this
		// route is reachable from those too since it's part of the "/"
		// mode-redirect flow, so guard rather than assume it's always set.
		if d.KioskEngine != nil {
			d.KioskEngine.Reset()
		}
		st := d.CurrentState()
		httpx.RenderPartial("ui/pages/self_order.html", map[string]any{
			"title":         "Self-order",
			"idleResetSecs": st.KioskIdleResetSeconds,
			"shopName":      d.Cfg.StoreName,
		})(w, r)
	})
}
