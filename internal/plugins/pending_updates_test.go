package plugins

import (
	"sync"
	"testing"
)

// restorePendingUpdates keeps this process-global out of every other test in
// the package.
func restorePendingUpdates(t *testing.T) {
	t.Helper()
	before := CurrentPendingUpdates().Count
	t.Cleanup(func() { SetPendingUpdates(before) })
}

func TestNotePendingUpdateAppliedDecrementsAndFloorsAtZero(t *testing.T) {
	restorePendingUpdates(t)

	SetPendingUpdates(2)
	NotePendingUpdateApplied()
	if got := CurrentPendingUpdates().Count; got != 1 {
		t.Fatalf("count = %d, want 1", got)
	}
	NotePendingUpdateApplied()
	if got := CurrentPendingUpdates().Count; got != 0 {
		t.Fatalf("count = %d, want 0", got)
	}

	// A manual update of a plugin the scheduler never counted (it hasn't
	// ticked yet, say) must not drive the chip's count negative.
	NotePendingUpdateApplied()
	if got := CurrentPendingUpdates().Count; got != 0 {
		t.Fatalf("count = %d, want 0 — the count must never go negative", got)
	}
	SetPendingUpdates(-3)
	if got := CurrentPendingUpdates().Count; got != 0 {
		t.Fatalf("count = %d, want 0 — SetPendingUpdates must clamp too", got)
	}
}

// TestNotePendingUpdateAppliedIsRaceSafe exercises the compare-and-swap
// against concurrent scheduler-style publishes: the manual Update button and
// StartPluginUpdateScheduler's tick genuinely run at the same time on a busy
// till. Run with -race, this catches a load-modify-store implementation.
func TestNotePendingUpdateAppliedIsRaceSafe(t *testing.T) {
	restorePendingUpdates(t)
	SetPendingUpdates(100)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			NotePendingUpdateApplied()
		}()
	}
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = CurrentPendingUpdates()
		}()
	}
	wg.Wait()

	if got := CurrentPendingUpdates().Count; got != 50 {
		t.Fatalf("count = %d, want 50 — 50 concurrent decrements from 100 must not lose one", got)
	}
}
