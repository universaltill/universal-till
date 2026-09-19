package pages

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// selfOrderSessionCookie carries a table-QR guest's session token
// (ut-docs#2261) — the only thing tying an anonymous browser to its table's
// own basket in d.KioskSessions. Path "/" (Tester finding, ut-docs#2261
// review — an earlier draft scoped it to "/self-order", which by RFC 6265
// path-matching is NOT a prefix of "/api/self-order/*" and so is never sent
// on any of the actual scan/cart/checkout mutation endpoints; confirmed with
// a real curl cookie jar, where every mutation silently fell back to the
// shared bare d.KioskEngine — the exact cross-table collision this card
// exists to remove). HttpOnly so it's meaningless to read from JS, and
// resolveOrderEngine is the only code that ever interprets it — a cashier
// route ignores it same as any other cookie it doesn't recognise.
// SameSite=Lax; no Secure flag, matching this codebase's auth.CookieName
// convention (auth_page.go, also Path "/") — a LAN till is plain HTTP.
const selfOrderSessionCookie = "self_order_session"

// kioskSessionSweepInterval is how often the background loop evicts
// idle-expired table sessions (memory hygiene only — Lookup already refuses
// an expired session on its own, see pos.TableSessions.Sweep).
const kioskSessionSweepInterval = 15 * time.Minute

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
		startURL := "/self-order/shop"
		// Table QR entry (ut-docs#815, reshaped by ut-docs#2261):
		// /self-order?table=<tables.id> no longer binds the ONE process-global
		// kiosk basket — it resolves (or creates) that table's OWN session in
		// d.KioskSessions and hands the browser a cookie naming it, so two
		// tables ordering at the same time never touch each other's basket.
		// The old cross-table "till busy" fail-safe that stood in for real
		// concurrency is gone with it: there is nothing left to collide.
		//
		// A guest revisiting ?table= (the shop page's idle-reset bounce, a
		// deliberate restart) or a SECOND device scanning the same printed QR
		// JOINS the table's live session — one shared cart per table, the
		// same model Toast/Square/Lightspeed use, and the only one under which
		// a table can split a bill. The join is surfaced as an inline banner
		// on the shop screen (?joined=1), never a blocking interstitial.
		//
		// The table is still carried on the engine's own TableID/TableLabel
		// (ADR-0054/ut-docs#820's existing pos.Service fields), just on the
		// per-table engine now. An invalid/unknown/disabled id, or no ?table
		// at all, falls through to the bare-kiosk path below — this never
		// errors the page out over a bad/stale/tampered QR value.
		if requestedTable := strings.TrimSpace(r.URL.Query().Get("table")); requestedTable != "" && d.KioskSessions != nil {
			if t, found, err := data.NewPOSRepo(d.Db).GetTable(r.Context(), requestedTable); err == nil && found && t.Enabled {
				token, engine, existed, err := d.KioskSessions.SessionForTable(t.ID)
				if err == nil {
					if !existed {
						// SetTable is a no-op unless the (fresh, empty)
						// basket's order type is dine-in, which it always is
						// on a just-minted engine, so this always takes
						// effect. A joined session already carries it.
						engine.SetTable(t.ID, t.Label)
					} else {
						startURL += "?joined=1"
					}
					http.SetCookie(w, &http.Cookie{
						Name:     selfOrderSessionCookie,
						Value:    token,
						Path:     "/",
						HttpOnly: true,
						SameSite: http.SameSiteLaxMode,
					})
					httpx.RenderPartial("ui/pages/self_order.html", map[string]any{
						"title":         httpx.T(httpx.RequestLocale(r), "page.title.self_order"),
						"idleResetSecs": st.KioskIdleResetSeconds,
						"shopName":      d.Cfg.StoreName,
						"startURL":      startURL,
					})(w, r)
					return
				}
			}
		}
		// Bare walk-up kiosk (no valid ?table=): UNCHANGED by ut-docs#2261.
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
		// A leftover table-session cookie from an earlier ?table= visit on
		// this browser must not make the walk-up flow act on that table's
		// basket: drop it, so every later request resolves to d.KioskEngine.
		// The table's session itself is left alone — another device at that
		// table may still be ordering on it.
		if _, err := r.Cookie(selfOrderSessionCookie); err == nil {
			http.SetCookie(w, &http.Cookie{
				Name:     selfOrderSessionCookie,
				Value:    "",
				Path:     "/",
				MaxAge:   -1,
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
			})
		}
		httpx.RenderPartial("ui/pages/self_order.html", map[string]any{
			"title":         httpx.T(httpx.RequestLocale(r), "page.title.self_order"),
			"idleResetSecs": st.KioskIdleResetSeconds,
			"shopName":      d.Cfg.StoreName,
			"startURL":      startURL,
		})(w, r)
	})
}

// resolveOrderEngine returns the basket this self-order request acts on
// (ut-docs#2261): the per-table session engine when the request carries a
// valid, live session cookie, else the bare-kiosk d.KioskEngine unchanged
// (today's behaviour). A cookie whose session is unknown or idle-evicted —
// or one that outlived a till restart, since the store is in-memory —
// never errors: it falls through exactly as if no cookie existed, the same
// graceful degradation the bare kiosk's idle-reset already has.
//
// The second result reports whether a table session was resolved, for the
// one caller (the shop page's joined banner) that must not trust the
// guest-controllable ?joined flag on its own.
func resolveOrderEngine(d *common.Deps, r *http.Request) (*pos.Service, bool) {
	if c, err := r.Cookie(selfOrderSessionCookie); err == nil && c.Value != "" {
		if engine, _, ok := d.KioskSessions.Lookup(c.Value); ok {
			return engine, true
		}
	}
	return d.KioskEngine, false
}

// StartKioskSessionSweep runs the periodic idle-session eviction for
// d.KioskSessions (ut-docs#2261). Shape mirrors StartFiscalSignReconcileSweep
// exactly: a goroutine, a ticker, wg.Done() on ctx.Done(). No initial delay
// — the store is empty at boot, so the first tick is a no-op either way.
func StartKioskSessionSweep(ctx context.Context, d *common.Deps, wg *sync.WaitGroup) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(kioskSessionSweepInterval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				d.KioskSessions.Sweep(time.Now())
			case <-ctx.Done():
				return
			}
		}
	}()
}
