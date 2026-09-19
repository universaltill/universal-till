package pages

import (
	"net/http"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// selfOrderTableBusyMaxIdle is the busy guard's own recency window
// (ut-docs#2261 review finding B1) — deliberately much shorter than
// selfOrderSessionMaxIdle (self_order_session_sweep.go's 2h memory-bound
// eviction). A session that has gone quiet this long no longer holds its
// table for the busy check: a guest who scans, adds an item, then changes
// their mind and orders at the counter instead used to leave that table's
// QR unscannable by anyone else for the full 2h sweep window, with no
// staff-facing way to clear it. The session itself is untouched — it stays
// in memory and resumable by its own cookie — this only narrows who counts
// as "holding" the table for a DIFFERENT phone's scan.
const selfOrderTableBusyMaxIdle = 10 * time.Minute

// selfOrderSessionCookie carries a table-bound guest's session token
// (ADR-0103 Decision 2, ut-docs#2261) — the key into
// common.Deps.SelfOrderSessions. Minted only by GET /self-order?table=<id>,
// cleared on completed checkout. HttpOnly + SameSite=Lax, the same flags
// auth_page.go's setSessionCookie uses for the till's own anonymous-flow
// continuity cookie.
const selfOrderSessionCookie = "ut_self_order_session"

// selfOrderSession resolves THIS request's basket engine and the session
// token it came from — the one seam every /self-order and /api/self-order/*
// handler goes through (ADR-0103 Decision 3). A request carrying a session
// cookie the manager still recognises gets that session's own *pos.Service
// (and Get touches its idle clock); anything else — no cookie at all (the
// bare walk-up/physical-kiosk path), a cookie the manager no longer knows
// (already checked out, or idle-swept), or a Deps with no manager (bare test
// harnesses) — gets d.KioskEngine and "" exactly as before this card. The
// token is what the checkout path needs to tell the two apart when the
// order completes (releaseSelfOrderSession).
func selfOrderSession(d *common.Deps, r *http.Request) (*pos.Service, string) {
	if d.SelfOrderSessions != nil {
		if c, err := r.Cookie(selfOrderSessionCookie); err == nil {
			if svc, ok := d.SelfOrderSessions.Get(c.Value); ok {
				return svc, c.Value
			}
		}
	}
	return d.KioskEngine, ""
}

// selfOrderEngine is selfOrderSession without the token — what the ~30
// existing `d.KioskEngine.X()` call sites in self_order_shop.go became.
func selfOrderEngine(d *common.Deps, r *http.Request) *pos.Service {
	svc, _ := selfOrderSession(d, r)
	return svc
}

// setSelfOrderSessionCookie writes (maxAge 0, a browser-session cookie) or
// clears (maxAge -1) the session cookie. Path is "/" rather than the
// narrower "/self-order": cookie path matching is prefix-based and the
// whole browse/cart/checkout flow lives under /api/self-order/*, which does
// NOT start with /self-order — a cookie scoped there would be withheld by
// every browser from exactly the handlers that need it, and each API call
// would silently fall back to the shared KioskEngine
// (TestSelfOrder_SessionCookieReachesAPIRoutes pins this). The value is an
// opaque random token only these handlers ever read, so the wider path
// carries nothing another surface could use.
func setSelfOrderSessionCookie(w http.ResponseWriter, token string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     selfOrderSessionCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// releaseSelfOrderSession ends a table-bound session once its order has
// completed (ADR-0103 Decision 5): the session is removed from the manager
// outright — not reset in place — so its table is free for the next scan
// immediately, and the cookie is cleared so the guest's next visit starts
// fresh. token "" (the request resolved to d.KioskEngine) is a no-op: the
// walk-up path keeps its own existing Reset. Must run before the response
// body is written (it sets a header).
func releaseSelfOrderSession(w http.ResponseWriter, d *common.Deps, token string) {
	if token == "" || d.SelfOrderSessions == nil {
		return
	}
	d.SelfOrderSessions.Remove(token)
	setSelfOrderSessionCookie(w, "", -1)
}

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
		// Two entry paths (ADR-0103 Decision 2, ut-docs#2261):
		//
		//  - ?table=<id> naming a valid, enabled table is a guest's own
		//    phone scanning a table QR (ut-docs#815). It gets a per-session
		//    basket from d.SelfOrderSessions — resumed when this browser
		//    already holds one for that table, minted otherwise, or blocked
		//    with the busy screen when a DIFFERENT browser already has a
		//    live, non-empty session on that same table (the one collision
		//    real per-session isolation can't dissolve: two phones at one
		//    table). See bindSelfOrderTableSession.
		//
		//  - No ?table= at all (or one that doesn't name a usable table) is
		//    the bare walk-up/physical-kiosk path, unchanged from ADR-0020:
		//    landing here always means "start fresh", so the till-global
		//    KioskEngine is reset — without this an abandoned cart would
		//    silently greet the NEXT customer instead of an empty one, since
		//    that basket is till-process-level state, not per-visit. Never a
		//    session, never a cookie.
		//
		// KioskEngine, not Engine (ut-docs#449): this route is reachable by
		// any LAN client in any display mode, so resetting the shared
		// cashier engine here used to wipe the till's live sale. Only the
		// kiosk's own basket may be cleared.
		// d.KioskEngine is nil in some page-level test harnesses that never
		// exercise the basket (e.g. TestSelfOrderModeRedirectsHome) — this
		// route is reachable from those too since it's part of the "/"
		// mode-redirect flow, so guard rather than assume it's always set.
		// d.SelfOrderSessions is nil in the same harnesses; with no manager
		// the table path can't exist, so such a request takes the walk-up
		// path (the bind helper isn't consulted at all).
		requestedTable := strings.TrimSpace(r.URL.Query().Get("table"))
		if d.KioskEngine != nil {
			bound := false
			if requestedTable != "" && d.SelfOrderSessions != nil {
				var busy bool
				bound, busy = bindSelfOrderTableSession(w, r, d, requestedTable, time.Now())
				if busy {
					httpx.RenderPartial("ui/pages/self_order.html", map[string]any{
						"title":    httpx.T(httpx.RequestLocale(r), "page.title.self_order"),
						"shopName": d.Cfg.StoreName,
						"Busy":     true,
					})(w, r)
					return
				}
			}
			if !bound {
				d.KioskEngine.Reset()
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

// bindSelfOrderTableSession is the ?table= half of GET /self-order
// (ADR-0103 Decisions 2 and 4). It reports (bound, busy):
//
//   - bound=false, busy=false: tableID doesn't name a valid, enabled table
//     (stale/reprinted/tampered QR) — nothing minted, no cookie; the caller
//     falls through to the plain walk-up path, exactly today's "leaves the
//     basket with no table, never errors the page out" behaviour.
//   - busy=true: the table already has a live, NON-empty, RECENTLY-ACTIVE
//     session owned by a different browser (two phones at one table).
//     Nothing minted; the caller renders the existing "till busy" screen
//     (ut-docs#815's own template, copy revised by ut-docs#2261 review
//     finding B2 for the narrowed trigger). An EMPTY session has nothing to
//     lose, so it never blocks, same threshold the old guard used. A
//     session idle past selfOrderTableBusyMaxIdle also never blocks — see
//     TableOwnerActive's own doc comment (ut-docs#2261 review finding B1):
//     an abandoned cart must not hold a table hostage for the full 2h
//     memory-bound sweep window.
//   - bound=true: this request now has a live session bound to the table —
//     resumed (this browser already held one for that table: the idle-reset
//     bounce, or a deliberate re-open — nothing reset, nothing re-minted,
//     the guest keeps their order) or freshly minted, with the cookie set.
//
// Table binding rides on the SAME TableID/TableLabel fields ADR-0054/
// ut-docs#820 gave every pos.Service (SetTable/TableID/TableLabel) — not a
// new mechanism. SetTable is a no-op unless the (fresh, empty) basket's
// order-type default is dine-in, which it always is here, so it always
// takes effect.
//
// A browser whose cookie names a live session for a DIFFERENT table (a
// phone that moved tables and scanned the new one) gets a fresh session for
// the new table, and its old one is removed: the cookie is about to be
// overwritten, so that old session could never be reached again — leaving
// it would only keep the old table "busy" for everyone else until the idle
// sweep.
func bindSelfOrderTableSession(w http.ResponseWriter, r *http.Request, d *common.Deps, tableID string, now time.Time) (bound, busy bool) {
	t, found, err := data.NewPOSRepo(d.Db).GetTable(r.Context(), tableID)
	if err != nil || !found || !t.Enabled {
		return false, false
	}
	current, currentToken := selfOrderSession(d, r)
	if currentToken != "" && current.TableID() == t.ID {
		return true, false // resume
	}
	if ownerToken, owner, ok := d.SelfOrderSessions.TableOwnerActive(t.ID, selfOrderTableBusyMaxIdle, now); ok && ownerToken != currentToken && len(owner.Lines()) > 0 {
		return false, true
	}
	if currentToken != "" {
		d.SelfOrderSessions.Remove(currentToken)
	}
	token, svc := d.SelfOrderSessions.Create()
	svc.SetTable(t.ID, t.Label)
	setSelfOrderSessionCookie(w, token, 0)
	return true, false
}
