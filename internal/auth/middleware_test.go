package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// ut-docs#1099: POST /api/settings/exit-to-os is the manager's escape hatch
// off a locked-down kiosk window — and the login screen is exactly where a
// till with no signed-in operator is stuck. The handler already carries its
// own live manager-PIN gate (AuthorizeManager, sharing the device-wide
// keypad lockout), which is the intended authorization; requiring a session
// on top of it locked the escape hatch behind the very screen it exists to
// escape. Same handler-authenticates-itself shape as /api/sync/pair-request.
//
// The exemption must stay exactly this one path: the rest of the
// /api/settings/* surface is session-gated manager tooling, and lookalike
// or deeper paths must not ride along.
func TestExitToOSIsExemptButSettingsSurfaceStaysGated(t *testing.T) {
	if !exempt("/api/settings/exit-to-os") {
		t.Error("/api/settings/exit-to-os is not exempt — a till sitting at the " +
			"login screen would be 401'd before the handler's own manager-PIN " +
			"check ever runs, leaving a kiosk with no escape at all")
	}
	for _, p := range []string{
		"/api/settings",
		"/api/settings/window-mode",
		"/api/settings/osk",
		"/api/settings/exit-to-os/extra",
		"/api/settings/exit-to-os-not-really",
	} {
		if exempt(p) {
			t.Errorf("%s must NOT be exempt — only exit-to-os carries its own live PIN gate", p)
		}
	}
}

// The same fact proven through the real middleware, not just the exempt()
// helper: a request with NO session cookie must reach the next handler
// rather than being answered 401 by the middleware itself.
func TestExitToOSReachableWithoutSessionCookie(t *testing.T) {
	reached := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusNoContent)
	})
	// svc is only consulted when a session cookie is present; this request
	// carries none, which is the whole point.
	h := Middleware(next, nil)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/settings/exit-to-os", nil))
	if !reached {
		t.Fatalf("middleware answered %d itself — the exit-to-os handler (and its own PIN check) was never reached", rec.Code)
	}
}

// /o/{token} (ut-docs#527): the customer order-tracking page is reached by
// scanning a QR on the self-order confirmation screen from a personal phone —
// there is no session and can never be one, same anonymous-customer shape as
// /self-order. The exemption must stay a real path-segment prefix: "/o/"
// only, never a bare "/o" and never a lookalike like "/o-not-really" — the
// same boundary-anchoring concern TestSyncPullPathsAreExempt pins for the
// pair-requests prefix.
func TestOrderTrackingRoutesAreExempt(t *testing.T) {
	for _, p := range []string{
		"/o/0123456789abcdef0123456789abcdef",
		"/o/0123456789abcdef0123456789abcdef/status",
		"/o/anything",
	} {
		if !exempt(p) {
			t.Errorf("%s is not exempt — a customer scanning the tracking QR would be bounced to /login", p)
		}
	}
	for _, p := range []string{
		"/o",
		"/o-not-really",
		"/orders", // the operator-facing order-status page must stay gated
	} {
		if exempt(p) {
			t.Errorf("%s must NOT be exempt — only /o/{token} paths are the anonymous tracking surface", p)
		}
	}
}

// Every machine-to-machine sync path the replica's pull/push loop calls must
// be exempt here, because this middleware runs BEFORE the handler's per-till
// bearer check. A path missing from the list is rejected 401 while the till is
// authenticating perfectly — which is not a failure mode anyone debugs
// quickly.
//
// ut-docs#388: /api/sync/stock was missing. Inventory therefore never reached
// any replica, silently and forever. The only symptom was a log line the shop
// owner never sees ("stock sync pull rejected: 401 Unauthorized" every 30s),
// while the second till simply showed no inventory. Found on a real two-till
// shop, not by any test.
//
// Listing the paths literally is the point: this is a hand-maintained
// allow-list, so the test has to be an independent statement of what the
// client needs, not a re-derivation of the same list.
func TestSyncPullPathsAreExempt(t *testing.T) {
	// Paths the replica calls with only a Bearer token (no session cookie).
	for _, p := range []string{
		"/api/sync/enroll",
		"/api/sync/ping",
		"/api/sync/snapshot",
		"/api/sync/sales",
		"/api/sync/admin",
		"/api/sync/stock",
		// ut-docs#460: the replica pull tick's plugin-set poll. Missing at
		// first — the exact /api/sync/stock failure class again: the till
		// authenticated perfectly and was still 401'd here, so plugin
		// propagation silently never worked at all.
		"/api/sync/plugins",
		"/api/sync/assets",
		"/api/sync/assets/file",
		"/api/setup/join",
		// ut-docs#537: a joining replica has no session on the primary at
		// all (it's a stranger LAN device, not yet enrolled), so both the
		// inbound pair request and the possession-gated status poll it
		// makes against the PRIMARY must be exempt — this is the same
		// class of gap as /api/sync/stock above, just found via the ADR-
		// 0033 pairing surface instead of the sync-pull loop.
		"/api/sync/pair-request",
		"/api/sync/pair-requests/some-request-id",
		// ut-docs#611: the desktop shell (unitill-desktop) reads this at
		// launch, before any operator has signed in, to decide which native
		// window mode to apply — same "no session exists yet" shape as the
		// pairing/sync paths above.
		"/api/window-mode",
	} {
		if !exempt(p) {
			t.Errorf("%s is not exempt — this middleware will 401 it before the "+
				"handler's bearer check runs, so replicas can never use it", p)
		}
	}

	// The exemption must stay narrow: these are operator surfaces and must
	// still require a session, or the allow-list becomes a hole.
	for _, p := range []string{
		"/api/sync/tills/list",
		"/api/sync/promote",
		"/api/sync/pair",
		"/api/inventory/low-stock",
		"/settings",
		// The manager-gated pending-requests LIST (no trailing slash/id) is
		// a completely different handler from the per-id possession-gated
		// GET above — it must stay behind a session, or any LAN caller
		// could read every pending device name + derived verification
		// code. Guards against the /api/sync/pair-requests/ prefix ever
		// being widened to swallow this bare path too.
		"/api/sync/pair-requests",
		// ut-docs#537 review: the manager-PIN-gated approve/deny actions
		// live under the SAME /api/sync/pair-requests/ prefix as the
		// possession-gated status GET, one path segment deeper
		// (/{id}/approve, /{id}/deny). A plain HasPrefix match would
		// exempt these too — turning manager approval, ADR-0033 §8's
		// stated trust boundary for inbound pairing, into an anonymous
		// LAN PIN-guessing oracle that also trips the device-wide login
		// lockout. Pins the segment-boundary fix, not just the bare-list
		// boundary above.
		"/api/sync/pair-requests/some-request-id/approve",
		"/api/sync/pair-requests/some-request-id/deny",
	} {
		if exempt(p) {
			t.Errorf("%s must NOT be exempt — it is an operator surface", p)
		}
	}
}
