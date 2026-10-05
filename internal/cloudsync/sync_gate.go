package cloudsync

// ADR-0148 (ut-docs#3615): the periodic check-in runs only for a paid
// store. A till that is not registered, claimed and on a plan that allows
// cloud_sync makes no background sync calls at all; it contacts sync only
// inside an operator check-in window that an operator action on this till
// opened (Register now, pairing, a registering plugin install, managed-TSE
// setup, "Check for a paid plan", a claim code). The window lives in memory
// only: a restart closes it. A check-in inside it caches the response's
// entitlement block, so a paid answer turns the loop on with no restart.
//
// Amendment 2026-10-05 (ut-docs#3673): an unpaid till with a store identity
// also makes one check-in attempt per start when its version or today's
// local date differs from what its last answered unpaid check-in recorded
// (first run, first start after an update, first start of the day),
// re-tested at each gated tick in the first hour after Start. Never a
// timer, never a retry. Only a 2xx or a 402 records both markers; any
// other answer or a transport failure records nothing, so the next
// trigger tries again. An unpaid check-in POSTs the device report directly
// (no conditional GET), and a 402 closes any open operator window.

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/entitlement"
	"github.com/universaltill/universal-till/internal/logging"
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

// closeOperatorWindow ends the operator window a 402 plan_required
// answered: no retry until the next trigger (ADR-0148 amendment §4,
// ut-docs#3673). seen is the deadline read when the tick began, so a
// window an operator opened while the request was in flight survives, and
// its kick still runs a check-in.
func closeOperatorWindow(seen int64) { operatorDeadlineNS.CompareAndSwap(seen, 0) }

// The version and local date of this till's last unpaid check-in the cloud
// answered (ADR-0148 amendment §1-2, ut-docs#3673).
const (
	keyUnpaidCheckinVersion = "cloudsync.unpaid_checkin.version"
	keyUnpaidCheckinDate    = "cloudsync.unpaid_checkin.date"
)

// startupCheckinWindow is how long after Start the start-up check-in may
// still fire: a till without a real-time clock (a Raspberry Pi) boots with
// yesterday's time until NTP corrects it (amendment §1).
const startupCheckinWindow = time.Hour

// startupCheckinArmedAt is when Start armed the start-up check-in (nil =
// disarmed). Measured on startupClock, whose time.Now readings carry Go's
// monotonic clock, so an NTP jump of the wall clock neither ends the hour
// early nor stretches it.
var startupCheckinArmedAt atomic.Pointer[time.Time]

// startupClock is the start-up hour's clock; tests pin it apart from
// operatorNow (the wall clock the date comes from).
var startupClock = time.Now

// armStartupCheckin arms the start-up check-in (Start, before its goroutine).
func armStartupCheckin() {
	at := startupClock()
	startupCheckinArmedAt.Store(&at)
}

// disarmStartupCheckin ends the start-up trigger for this run.
func disarmStartupCheckin() { startupCheckinArmedAt.Store(nil) }

// takeStartupCheckin is a gated tick's start-up test (amendment §1): while
// armed and within startupCheckinWindow, it re-checks unpaidCheckinDue at
// every gated tick; the first time it is due it disarms and says yes, so
// the till makes exactly one attempt per start whatever the outcome. Past
// the hour it disarms with no call.
func takeStartupCheckin(ctx context.Context, settings *data.SettingsRepo, now time.Time) bool {
	armed := startupCheckinArmedAt.Load()
	if armed == nil {
		return false
	}
	if startupClock().Sub(*armed) >= startupCheckinWindow {
		startupCheckinArmedAt.CompareAndSwap(armed, nil)
		return false
	}
	if !unpaidCheckinDue(ctx, settings, now) {
		return false
	}
	return startupCheckinArmedAt.CompareAndSwap(armed, nil)
}

// unpaidCheckinDay is now's local calendar date, as the markers store it.
func unpaidCheckinDay(now time.Time) string { return now.Local().Format("2006-01-02") }

// unpaidCheckinDue reports whether a start-up check-in is due for an unpaid
// till: the running version or today's date differs from the recorded one
// (nothing recorded = first run). A settings read error says no: quiet, no
// call.
func unpaidCheckinDue(ctx context.Context, settings *data.SettingsRepo, now time.Time) bool {
	version, _, err := settings.Get(ctx, keyUnpaidCheckinVersion)
	if err != nil {
		return false
	}
	day, _, err := settings.Get(ctx, keyUnpaidCheckinDate)
	if err != nil {
		return false
	}
	return version != buildinfo.Version || day != unpaidCheckinDay(now)
}

// recordUnpaidCheckin records that the cloud answered an unpaid check-in.
// Best-effort: a failed write only means the next start checks in again.
func recordUnpaidCheckin(ctx context.Context, settings *data.SettingsRepo, now time.Time) {
	if err := settings.SetMany(ctx, map[string]string{
		keyUnpaidCheckinVersion: buildinfo.Version,
		keyUnpaidCheckinDate:    unpaidCheckinDay(now),
	}); err != nil {
		logging.L().Warnf("cloudsync: unpaid check-in not recorded (the next start checks in again): %v", err)
	}
}
