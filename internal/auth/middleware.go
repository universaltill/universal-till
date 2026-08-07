package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

type ctxKey struct{}

// Disabled reports whether the middleware is off (UT_AUTH=off escape hatch
// for CI/dev tooling); read once at startup by pages.Init.
func Disabled(env string) bool {
	return strings.EqualFold(strings.TrimSpace(env), "off")
}

// exempt paths never require a session: the login flow itself, static
// assets the login page needs, and health checks.
func exempt(path string) bool {
	// /setup + /api/setup are the first-boot wizard; both refuse to run once
	// an operator exists, so exempting them leaks nothing.
	if path == "/login" || path == "/healthz" || path == "/setup" || path == "/api/setup" {
		return true
	}
	// Machine-to-machine sync surface (ADR-0011): enroll is one-time-token
	// authed; ping/snapshot/sales/admin are per-till-bearer authed —
	// enforced in the handlers. The /api/setup/* wizard routes (manual join
	// plus the first-boot discovery/pairing trio, ut-docs#289) all refuse
	// once an operator exists — a brand-new till has no operators, so no
	// session can exist and these could never be reached otherwise.
	switch path {
	case "/api/sync/enroll", "/api/sync/ping", "/api/sync/snapshot", "/api/sync/sales", "/api/sync/admin",
		"/api/sync/assets", "/api/sync/assets/file", "/api/setup/join",
		"/api/setup/discover-primaries", "/api/setup/pair-start", "/api/setup/pair-status":
		return true
	}
	// /self-order (ADR-0020): the self-order kiosk flow is used by
	// anonymous walk-up customers who can't PIN-login — the whole surface
	// (landing page + its own future browse/cart/checkout API routes) is
	// exempt. Prefixed, not an exact match, since Phase 3/4 add API routes
	// under this same path. Kiosk sales attribute to the seeded "kiosk"
	// user (migration 018), not a session. Nothing else about the till is
	// exempt — a manager needing admin access still goes through /login.
	if path == "/self-order" || strings.HasPrefix(path, "/self-order/") || strings.HasPrefix(path, "/api/self-order/") {
		return true
	}
	for _, p := range []string{"/api/auth/", "/public/", "/themes/", "/plugin-icons/"} {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// Middleware gates every route behind a live session. Browsers are redirected
// to /login; API calls get 401 JSON. The operator lands in the context.
func Middleware(next http.Handler, svc *Service) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if exempt(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if c, err := r.Cookie(CookieName); err == nil {
			if u, ok := svc.Resolve(r.Context(), c.Value); ok {
				next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, u)))
				return
			}
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": nil, "error": map[string]string{"code": "unauthorized", "message": "sign in required"},
			})
			return
		}
		// HTMX fragment loads (nav chips, basket polls…) must NOT get a 302:
		// htmx follows it transparently and swaps the ENTIRE login page into
		// the fragment's slot — the PIN pad rendered inside the header bar.
		// HX-Redirect makes htmx do a real browser navigation to /login.
		if r.Header.Get("HX-Request") == "true" {
			w.Header().Set("HX-Redirect", "/login")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	})
}

// FromContext returns the operator attached by the middleware.
func FromContext(ctx context.Context) (User, bool) {
	u, ok := ctx.Value(ctxKey{}).(User)
	return u, ok
}

// UserID returns the session operator's id, or "system" when auth is
// disabled (UT_AUTH=off keeps till-initiated writes attributable).
func UserID(r *http.Request) string {
	if u, ok := FromContext(r.Context()); ok {
		return u.ID
	}
	return "system"
}

// WithUser returns a request carrying the given operator — test helper and
// seam for non-middleware entry points.
func WithUser(r *http.Request, u User) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), ctxKey{}, u))
}
