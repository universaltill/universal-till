package plugins

import (
	"sync"
	"testing"
)

// restorePendingUpdates keeps this process-global out of every other test in
// the package.
func restorePendingUpdates(t *testing.T) {
	t.Helper()
	before := CurrentPendingUpdates()
	t.Cleanup(func() { PublishPendingUpdates(before) })
}

func TestNotePendingUpdateAppliedDecrementsAndFloorsAtZero(t *testing.T) {
	restorePendingUpdates(t)

	PublishPendingUpdates(PendingUpdateStatus{Count: 2, LanguagePending: false})
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
	PublishPendingUpdates(PendingUpdateStatus{Count: -3, LanguagePending: false})
	if got := CurrentPendingUpdates().Count; got != 0 {
		t.Fatalf("count = %d, want 0 — PublishPendingUpdates must clamp too", got)
	}
}

// TestSetPendingUpdatesClampsLanguagePendingAtZeroCount pins down the
// invariant PublishPendingUpdates and NotePendingUpdateApplied must both keep:
// LanguagePending can never be true while Count is 0 — a "language pack
// update available" chip with nothing actually pending would be a lie a
// merchant can never resolve by tapping it (ut-docs#2299).
func TestPublishPendingUpdatesClampsLanguagePendingAtZeroCount(t *testing.T) {
	restorePendingUpdates(t)

	PublishPendingUpdates(PendingUpdateStatus{Count: 0, LanguagePending: true})
	if got := CurrentPendingUpdates(); got.Count != 0 || got.LanguagePending {
		t.Fatalf("status = %+v, want {0 false} — languagePending must clamp to false at count 0", got)
	}

	PublishPendingUpdates(PendingUpdateStatus{Count: -1, LanguagePending: true})
	if got := CurrentPendingUpdates(); got.Count != 0 || got.LanguagePending {
		t.Fatalf("status = %+v, want {0 false} — a negative count clamp must also clear languagePending", got)
	}
}

// TestNotePendingUpdateAppliedPreservesLanguagePendingUntilZero: decrementing
// a count that still has other pending updates left must not silently drop
// the "a language pack is among them" signal — only reaching zero clears it.
func TestNotePendingUpdateAppliedPreservesLanguagePendingUntilZero(t *testing.T) {
	restorePendingUpdates(t)

	PublishPendingUpdates(PendingUpdateStatus{Count: 2, LanguagePending: true})
	NotePendingUpdateApplied()
	if got := CurrentPendingUpdates(); got.Count != 1 || !got.LanguagePending {
		t.Fatalf("status = %+v, want {1 true} — languagePending must survive a decrement that leaves Count > 0", got)
	}
	NotePendingUpdateApplied()
	if got := CurrentPendingUpdates(); got.Count != 0 || got.LanguagePending {
		t.Fatalf("status = %+v, want {0 false} — reaching zero must clear languagePending too", got)
	}
}

// TestNotePendingUpdateAppliedIsRaceSafe exercises the compare-and-swap
// against concurrent scheduler-style publishes: the manual Update button and
// StartPluginUpdateScheduler's tick genuinely run at the same time on a busy
// till. Run with -race, this catches a load-modify-store implementation.
func TestNotePendingUpdateAppliedIsRaceSafe(t *testing.T) {
	restorePendingUpdates(t)
	PublishPendingUpdates(PendingUpdateStatus{Count: 100, LanguagePending: false})

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
