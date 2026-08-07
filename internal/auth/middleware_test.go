package auth

import "testing"

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
		"/api/sync/assets",
		"/api/sync/assets/file",
		"/api/setup/join",
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
	} {
		if exempt(p) {
			t.Errorf("%s must NOT be exempt — it is an operator surface", p)
		}
	}
}
