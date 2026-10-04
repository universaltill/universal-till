package cloudsync

// ADR-0148 (ut-docs#3615): the periodic check-in runs only for a paid
// store. A till that is not registered, claimed and on a plan that allows
// cloud_sync makes no background sync calls at all; it contacts sync only
// inside an operator check-in window that an operator action on this till
// opened (Register now, pairing, a registering plugin install, managed-TSE
// setup, "Check for a paid plan", a claim code). The window lives in memory
// only: a restart closes it. A check-in inside it caches the response's
// entitlement block, so a paid answer turns the loop on with no restart.

import (
	"sync/atomic"
	"time"

	"github.com/universaltill/universal-till/internal/entitlement"
)

// syncAllowedFn is the paid-only gate; a package var so this package's
// pre-gate tick tests can run with the gate open (main_test.go).
var syncAllowedFn = entitlement.SyncAllowed

// operatorDeadlineNS is the operator window's deadline (UnixNano; 0 = never
// opened). Written from request goroutines, read on Start's goroutine.
var operatorDeadlineNS atomic.Int64

// operatorNow is the window's clock.
var operatorNow = time.Now

// OpenOperatorWindow lets this till check in for the next d even when the
// gate says no (ADR-0148 §2). It only ever extends: a 2-minute window
// opened during a 15-minute claim-code window leaves the longer one.
func OpenOperatorWindow(d time.Duration) {
	if d <= 0 {
		return
	}
	want := operatorNow().Add(d).UnixNano()
	for {
		cur := operatorDeadlineNS.Load()
		if cur >= want || operatorDeadlineNS.CompareAndSwap(cur, want) {
			return
		}
	}
}

// operatorWindowOpen reports whether an operator window is open at now.
func operatorWindowOpen(now time.Time) bool {
	return now.UnixNano() < operatorDeadlineNS.Load()
}
