package plugins

import (
	"testing"
)

// restorePendingUpdates keeps this process-global out of every other test in
// the package.
func restorePendingUpdates(t *testing.T) {
	t.Helper()
	before := CurrentPendingUpdates()
	t.Cleanup(func() { PublishPendingUpdates(before) })
}

// TestSetPendingUpdatesClampsLanguagePendingAtZeroCount pins down the
// invariant PublishPendingUpdates must keep: LanguagePending can never be
// true while Count is 0 — a "language pack update available" chip with
// nothing actually pending would be a lie a merchant can never resolve by
// tapping it (ut-docs#2299).
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
