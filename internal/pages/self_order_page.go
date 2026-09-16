package pages

import (
	"net/http"
	"strings"

	"github.com/universaltill/universal-till/internal/data"
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
		requestedTable := strings.TrimSpace(r.URL.Query().Get("table"))
		if d.KioskEngine != nil {
			// Busy guard (ut-docs#815 review finding — a different table's
			// guest scanning their own QR silently wiped an in-progress
			// table's basket AND rebound the session to the new table,
			// reproduced for real: table A's guest loses their order, and
			// table B's guest's checkout lands on table A's floor-plan
			// tile). KioskEngine is one till-process-global basket
			// (ADR-0020, scripts/ci/guard-kiosk-engine.sh) — correct for
			// one physical kiosk, not safe for two different tables' own-
			// phone sessions landing here around the same time. This does
			// NOT make ordering concurrent — that needs a genuinely
			// per-session basket, a real architecture change (see
			// ut-docs#2261) — it only makes the single-basket limitation
			// FAIL SAFE: a second table's guest sees a clear "till busy"
			// message instead of silently destroying the first guest's
			// order. Scoped narrowly: a DIFFERENT, non-empty ?table=
			// colliding with an active (non-empty) table-bound basket. The
			// guest re-scanning their OWN table's code always proceeds —
			// that's both a deliberate restart and the idle-reset bounce
			// (self_order_shop.go's idleResetURL), not a collision. A bare
			// kiosk hit (no ?table= at all) is deliberately left unchanged:
			// that is the physical kiosk's own long-standing "always start
			// fresh" behaviour (ADR-0020) and a different, pre-existing
			// risk this narrow fix does not attempt to solve — also
			// ut-docs#2261's territory, not this card's.
			activeTable := d.KioskEngine.TableID()
			if requestedTable != "" && activeTable != "" && requestedTable != activeTable && len(d.KioskEngine.Lines()) > 0 {
				httpx.RenderPartial("ui/pages/self_order.html", map[string]any{
					"title":    httpx.T(httpx.RequestLocale(r), "page.title.self_order"),
					"shopName": d.Cfg.StoreName,
					"Busy":     true,
				})(w, r)
				return
			}
			d.KioskEngine.Reset()
			// Table QR entry (ut-docs#815): /self-order?table=<tables.id>
			// binds this fresh basket to a physical table for the rest of
			// the session, carried on d.KioskEngine itself via the SAME
			// TableID/TableLabel fields ADR-0054/ut-docs#820 already gave
			// every pos.Service (SetTable/TableID/TableLabel,
			// internal/pos/service.go) — not a new mechanism, the kiosk
			// basket just uses the one the cashier engine already has.
			// SetTable is a no-op unless the (just-Reset, empty) basket's
			// order-type default is dine-in, which it always is here, so
			// this always takes effect. An invalid/unknown/disabled table
			// id, or no ?table param at all, leaves the basket with no
			// table — today's unchanged behaviour — this never errors the
			// page out over a bad/stale/tampered QR value.
			if requestedTable != "" {
				if t, found, err := data.NewPOSRepo(d.Db).GetTable(r.Context(), requestedTable); err == nil && found && t.Enabled {
					d.KioskEngine.SetTable(t.ID, t.Label)
				}
			}
		}
		st := d.CurrentState()
		httpx.RenderPartial("ui/pages/self_order.html", map[string]any{
			"title":         httpx.T(httpx.RequestLocale(r), "page.title.self_order"),
			"idleResetSecs": st.KioskIdleResetSeconds,
			"shopName":      d.Cfg.StoreName,
		})(w, r)
	})
}
