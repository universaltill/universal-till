package pages

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// selfOrderSessionCookie carries a table-bound guest's session token
// (ADR-0103 Decision 2, ut-docs#2261) — the key into
// common.Deps.SelfOrderSessions. No production path mints a session since
// ut-docs#4338 (table ordering moved to the cloud), so this resolution is
// inert; removal is a follow-up card (see ut-docs#4338). Was minted by GET
// /self-order?table=<id>, cleared on completed checkout. HttpOnly + SameSite=Lax, the same flags
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
	wireAbandonedTenderReversal(d) // table sessions hand off a dropped tender (ut-docs#6601)
	mux.HandleFunc("GET /self-order", func(w http.ResponseWriter, r *http.Request) {
		// Landing here always means "start fresh" (ADR-0020): the till-global
		// KioskEngine is reset — without this an abandoned cart would
		// silently greet the NEXT customer instead of an empty one, since
		// that basket is till-process-level state, not per-visit. A ?table=
		// is ignored: table ordering moved to the cloud's /t/<token> page
		// (ADR-0159 §2, ut-docs#4338), so this route never binds a table,
		// mints a session or sets a cookie any more.
		//
		// KioskEngine, not Engine (ut-docs#449): this route is reachable
		// without a login, so resetting the shared cashier engine here used
		// to wipe the till's live sale. Only the kiosk's own basket may be
		// cleared. d.KioskEngine is nil in some page-level test harnesses
		// that never exercise the basket (e.g. TestSelfOrderModeRedirectsHome)
		// — this route is reachable from those too since it's part of the "/"
		// mode-redirect flow, so guard rather than assume it's always set.
		if d.KioskEngine != nil {
			d.KioskEngine.Reset()
		}
		// The start screen also ends a customer's language pick
		// (ut-docs#4200): the idle timeout, the confirmation screen's
		// auto-return and Back all land here, and the next customer must
		// meet the shop's language.
		if _, err := r.Cookie(httpx.SelfOrderLocaleCookie); err == nil {
			httpx.ClearSelfOrderLocale(w)
			// This response must render in the shop's language too,
			// not the pick the request still carries.
			r = withoutSelfOrderLocale(r)
		}
		locale := httpx.RequestLocale(r)
		st := d.CurrentState()
		httpx.RenderPartial("ui/pages/self_order.html", map[string]any{
			"title":         httpx.T(locale, "page.title.self_order"),
			"idleResetSecs": st.KioskIdleResetSeconds,
			"shopName":      kioskShopName(r.Context(), d),
			"languages":     selfOrderLanguages(locale),
		})(w, r)
	})

	// GET /self-order/language?code=<locale> — the start screen's language
	// picker (ut-docs#4200). One tap starts the order in that language;
	// the pick lives in a browser-session cookie that only the self-order
	// routes read (httpx.SelfOrderLocaleCookie). Auth-exempt like the rest
	// of /self-order/*, so the code is checked against the installed
	// languages and anything else is dropped. A GET link, not a form: the
	// only state it changes is the visitor's own cookie.
	mux.HandleFunc("GET /self-order/language", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		if !httpx.InstalledLocale(code) {
			http.Redirect(w, r, "/self-order", http.StatusSeeOther)
			return
		}
		if baseLang(code) == baseLang(httpx.DefaultLocale()) {
			// The shop's own language: drop the pick rather than store the
			// bare tag (en for an en-GB shop), so money and number
			// formatting keep the shop's regional tag.
			httpx.ClearSelfOrderLocale(w)
		} else {
			httpx.WriteSelfOrderLocale(w, code)
		}
		http.Redirect(w, r, "/self-order/shop", http.StatusSeeOther)
	})
}

// selfOrderLanguage is one start-screen picker button.
type selfOrderLanguage struct {
	Code       string
	NativeName string
	Current    bool
}

// selfOrderLanguages lists every installed language by its own name, for
// the start screen's picker (ut-docs#4200). nil with fewer than two: there
// is nothing to pick. No flags — a flag names a country, not a language.
func selfOrderLanguages(current string) []selfOrderLanguage {
	codes := httpx.AvailableLocales()
	if len(codes) < 2 {
		return nil
	}
	// The shop's language is often a regional tag (en-GB) while installed
	// codes are bare (en): mark the exact match, else the first code with
	// the same base language — installedLocaleFor's rule, from the other
	// side.
	mark := slices.Index(codes, current)
	if mark < 0 {
		mark = slices.IndexFunc(codes, func(c string) bool { return baseLang(c) == baseLang(current) })
	}
	out := make([]selfOrderLanguage, 0, len(codes))
	for i, c := range codes {
		out = append(out, selfOrderLanguage{Code: c, NativeName: httpx.NativeLanguageName(c), Current: i == mark})
	}
	return out
}

// withoutSelfOrderLocale is r minus its ut_self_order_lang cookie, for the
// response that clears it.
func withoutSelfOrderLocale(r *http.Request) *http.Request {
	r2 := r.Clone(r.Context())
	r2.Header.Del("Cookie")
	for _, c := range r.Cookies() {
		if c.Name != httpx.SelfOrderLocaleCookie {
			r2.AddCookie(c)
		}
	}
	return r2
}

// kioskShopName is the name the kiosk header shows: store.name read live
// (a rename in Settings, from my. or in the setup wizard shows on the next
// page load, ut-docs#3020), falling back to the boot-time cfg.StoreName
// while the setting is unset, unreadable or still a placeholder (migration
// 001 seeds "My Store", which must not hide a UT_STORE_NAME name). A
// placeholder UT_STORE_NAME too (its default is "My Store") yields "": the
// kiosk then shows no name rather than an English placeholder (ut-docs#5773).
func kioskShopName(ctx context.Context, d *common.Deps) string {
	if v := shopNameOrEmpty(ctx, d); v != "" {
		return v
	}
	if !config.IsPlaceholderStoreName(d.Cfg.StoreName) {
		return strings.TrimSpace(d.Cfg.StoreName)
	}
	return ""
}
