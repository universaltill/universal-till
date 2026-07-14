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
	// authed, ping is per-till-bearer authed — enforced in the handlers.
	if path == "/api/sync/enroll" || path == "/api/sync/ping" {
		return true
	}
	for _, p := range []string{"/api/auth/", "/public/", "/themes/"} {
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
