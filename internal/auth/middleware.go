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
	// /api/window-mode (ut-docs#611): the desktop shell reads this at
	// launch, before any operator has signed in — same shape as /healthz,
	// and no more sensitive (UI display preferences, not shop data).
	if path == "/login" || path == "/healthz" || path == "/setup" || path == "/api/setup" || path == "/api/window-mode" {
		return true
	}
	// Machine-to-machine sync surface (ADR-0011): enroll is one-time-token
	// authed; ping/snapshot/sales/admin are per-till-bearer authed —
	// enforced in the handlers. The /api/setup/* wizard routes (manual join
	// plus the first-boot discovery/pairing trio, ut-docs#289) all refuse
	// once an operator exists — a brand-new till has no operators, so no
	// session can exist and these could never be reached otherwise.
	//
	// KEEP IN SYNC with what the replica's pull/push loop actually calls: a
	// machine-to-machine path missing from this list is rejected 401 by THIS
	// middleware before its handler's bearer check ever runs, so the till
	// authenticates perfectly and is still refused. /api/sync/stock was
	// missing, which meant inventory silently never reached any replica —
	// found in the field on a real two-till shop, where till 2 showed "no
	// inventory" forever while the only symptom was a log line
	// ("stock sync pull rejected: 401 Unauthorized") no shop owner ever sees.
	// TestSyncPullPathsAreExempt pins the list against the client.
	switch path {
	case "/api/sync/enroll", "/api/sync/ping", "/api/sync/snapshot", "/api/sync/sales", "/api/sync/admin",
		"/api/sync/stock", "/api/sync/plugins", "/api/sync/assets", "/api/sync/assets/file", "/api/setup/join",
		"/api/setup/discover-primaries", "/api/setup/pair-start", "/api/setup/pair-status",
		// ut-docs#1092: the wizard's step-1 catalog-language install POST —
		// same shape as the rest of this /api/setup/* group: it refuses
		// once an operator exists (NeedsFirstBoot, checked in the handler
		// itself), and a brand-new till has no operator to hold a session
		// in the first place.
		"/api/setup/language",
		// ut-docs#537: a joining replica has no session on the primary at
		// all — it's a stranger LAN device sending its first-ever request
		// there. The inbound pair request is unauthenticated by design
		// (ADR-0033 §8; rate-limited + sha256-commitment-gated in the
		// handler itself).
		"/api/sync/pair-request":
		return true
	}
	// /api/settings/exit-to-os (ut-docs#1099): the manager's escape hatch off
	// a locked-down kiosk window — and the login screen is exactly where a
	// till with no signed-in operator is stuck, so a session requirement here
	// locked the escape hatch behind the very screen it exists to escape.
	// Same handler-authenticates-itself shape as /api/sync/pair-request
	// above: the handler's own LIVE manager-PIN check (AuthorizeManager,
	// sharing the device-wide keypad lockout) is the real gate, by product-
	// owner requirement (#549 — a session cookie alone was never enough for
	// this action anyway). TestExitToOSIsExemptButSettingsSurfaceStaysGated
	// pins that the rest of /api/settings/* stays session-gated.
	if path == "/api/settings/exit-to-os" {
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
	// /o/{token} (ut-docs#527): the customer order-tracking page, reached by
	// scanning the QR on the self-order confirmation screen from a personal
	// phone — an anonymous customer with no session and no way to PIN-login,
	// same reasoning as /self-order above. Read-only and single-purpose: the
	// handlers expose an order's STATUS only, gated by the unguessable
	// tracking token itself (128 random bits), never by a session. Bare "/o"
	// is deliberately NOT exempt — only token-carrying paths exist here.
	if strings.HasPrefix(path, "/o/") {
		return true
	}
	for _, p := range []string{"/api/auth/", "/public/", "/themes/", "/plugin-icons/"} {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	// The possession-gated per-id status poll a joining replica makes while
	// waiting for approval (pairing_join.go's pairStatusHandler): GET
	// /api/sync/pair-requests/{id} — same unauthenticated-by-design
	// endpoint class as pair-request above, gated in the handler by the
	// request_secret, not a session.
	//
	// This MUST be bounded to exactly one path segment after the prefix —
	// pairing_api.go registers /api/sync/pair-requests/{id}/approve and
	// /deny under this same prefix, and those stay manager-PIN-gated
	// (authorizePairingManager) behind a REQUIRED session; a plain
	// HasPrefix match would exempt them too, turning approve/deny into an
	// anonymous LAN PIN-guessing oracle that also trips the device-wide
	// login lockout (independent review, ut-docs#537 — caught before
	// merge). The bare /api/sync/pair-requests LIST (no id) must also stay
	// gated, or any LAN caller could read every pending device name +
	// derived verification code. TestSyncPullPathsAreExempt pins all three
	// exclusions.
	if rest, ok := strings.CutPrefix(path, "/api/sync/pair-requests/"); ok && rest != "" && !strings.Contains(rest, "/") {
		return true
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
