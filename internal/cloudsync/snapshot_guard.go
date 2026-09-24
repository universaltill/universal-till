package cloudsync

import (
	"sync"
	"time"
)

// Guards around the catalog snapshot push (review findings 3 and 5 on the
// manage-shop catalog directives, contract §3.7): the snapshot is the whole
// catalog, so a cloud that refuses it must not get the full body again on
// every 2-minute tick with a warning each time.

// maxSnapshotBytes is the cloud's schema-2 ingest cap (16 MiB, contract
// §3.7). A larger snapshot is never posted: the cloud would answer 413.
// A var only so a test can lower it.
var maxSnapshotBytes = 16 << 20

const (
	// snapshotBackoffBase is the wait after the first refused (413) push;
	// it doubles per consecutive refusal up to snapshotBackoffMax.
	snapshotBackoffBase = 5 * time.Minute
	snapshotBackoffMax  = 6 * time.Hour
)

// snapshotNow is the guard's clock; a test swaps it.
var snapshotNow = time.Now

// snapshotGuardState is process-local: Start's loop is one goroutine, but
// Tick is also called from tests and the directive path, so it is locked.
var snapshotGuardState struct {
	mu          sync.Mutex
	failures    int
	nextAttempt time.Time
	// logged is the distinct failure last logged ("oversize", "413"), so
	// a repeat of the same failure is not logged again until a success or
	// a different failure.
	logged string
}

func resetSnapshotGuard() {
	snapshotGuardState.mu.Lock()
	defer snapshotGuardState.mu.Unlock()
	snapshotGuardState.failures = 0
	snapshotGuardState.nextAttempt = time.Time{}
	snapshotGuardState.logged = ""
}

// snapshotBackoff is the wait after the n-th consecutive refusal (n ≥ 1):
// base, 2×base, 4×base, … capped at snapshotBackoffMax.
func snapshotBackoff(n int) time.Duration {
	d := snapshotBackoffBase
	for i := 1; i < n; i++ {
		d *= 2
		if d >= snapshotBackoffMax {
			return snapshotBackoffMax
		}
	}
	return d
}

// snapshotInBackoff reports whether a refused push is still waiting out
// its backoff.
func snapshotInBackoff() bool {
	snapshotGuardState.mu.Lock()
	defer snapshotGuardState.mu.Unlock()
	return snapshotNow().Before(snapshotGuardState.nextAttempt)
}

// snapshotRefused records a refusal and schedules the next attempt. It
// reports whether this failure kind is new (so the caller logs it once).
func snapshotRefused(kind string) (wait time.Duration, first bool) {
	snapshotGuardState.mu.Lock()
	defer snapshotGuardState.mu.Unlock()
	snapshotGuardState.failures++
	wait = snapshotBackoff(snapshotGuardState.failures)
	snapshotGuardState.nextAttempt = snapshotNow().Add(wait)
	first = snapshotGuardState.logged != kind
	snapshotGuardState.logged = kind
	return wait, first
}

// snapshotOversize records an over-cap snapshot (no post, no backoff: the
// next tick rebuilds it and may fit). It reports whether to log.
func snapshotOversize() bool {
	snapshotGuardState.mu.Lock()
	defer snapshotGuardState.mu.Unlock()
	first := snapshotGuardState.logged != "oversize"
	snapshotGuardState.logged = "oversize"
	return first
}

// snapshotSucceeded clears the backoff and the logged failure.
func snapshotSucceeded() { resetSnapshotGuard() }

// maxSatelliteSkipLogged bounds the set of directive ids a satellite has
// already logged as skipped. When full it is cleared (a directive may then
// be logged once more — harmless, and memory stays bounded).
const maxSatelliteSkipLogged = 256

var satelliteSkipLog struct {
	mu   sync.Mutex
	seen map[string]struct{}
}

// firstSatelliteSkip reports whether this is the first time this process
// skips directive id as main-till only (finding 5: log once per directive,
// not every tick while it stays pending for the main till).
func firstSatelliteSkip(id string) bool {
	satelliteSkipLog.mu.Lock()
	defer satelliteSkipLog.mu.Unlock()
	if _, ok := satelliteSkipLog.seen[id]; ok {
		return false
	}
	if satelliteSkipLog.seen == nil || len(satelliteSkipLog.seen) >= maxSatelliteSkipLogged {
		satelliteSkipLog.seen = make(map[string]struct{})
	}
	satelliteSkipLog.seen[id] = struct{}{}
	return true
}

func resetSatelliteSkipLog() {
	satelliteSkipLog.mu.Lock()
	defer satelliteSkipLog.mu.Unlock()
	satelliteSkipLog.seen = nil
}

func satelliteSkipLoggedLen() int {
	satelliteSkipLog.mu.Lock()
	defer satelliteSkipLog.mu.Unlock()
	return len(satelliteSkipLog.seen)
}
