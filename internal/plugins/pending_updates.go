package plugins

import "sync/atomic"

// PendingUpdateStatus is the last-known count of installed-plugin updates
// still waiting on a merchant decision — i.e. every update
// StartPluginUpdateScheduler's last tick found MINUS any it auto-applied
// itself (a language pack is content, not code, and auto-applies silently;
// see ut-docs#1953). Mirrors internal/updates.Status's atomic-value pattern
// so the till can show a status-bar chip without a per-request DB query.
type PendingUpdateStatus struct {
	Count int
	// LanguagePending is true when at least one pending update (Count > 0)
	// is a language pack that this tick did NOT auto-apply — the two cases
	// where "content, not code" still leaves one waiting: a joined till
	// (ut-docs#460, joined tills never apply locally, they wait for the
	// main till) or an auto-apply that itself failed. Distinct from Count
	// because "N plugin updates available" doesn't tell a merchant that a
	// stale German/Turkish/... UI is specifically what's behind (ut-docs#2299).
	LanguagePending bool
}

var pendingUpdateState atomic.Value // PendingUpdateStatus

// CurrentPendingUpdates returns the last-known pending-update status (the
// zero value before the background scheduler's first tick).
func CurrentPendingUpdates() PendingUpdateStatus {
	if s, ok := pendingUpdateState.Load().(PendingUpdateStatus); ok {
		return s
	}
	return PendingUpdateStatus{}
}

// SetPendingUpdates records the outcome of one scheduler tick. Exported so
// internal/pages' StartPluginUpdateScheduler (which owns the DB/catalog
// access needed to actually run the check) can publish the result here for
// the status-chip template funcs to read. languagePending is ignored (forced
// false) whenever count is clamped to zero, so the two fields can never
// disagree about there being anything pending at all.
func SetPendingUpdates(count int, languagePending bool) {
	if count < 0 {
		count = 0
	}
	if count == 0 {
		languagePending = false
	}
	pendingUpdateState.Store(PendingUpdateStatus{Count: count, LanguagePending: languagePending})
}

// NotePendingUpdateApplied decrements the published count by one, never
// below zero. The scheduler only recomputes every 15 minutes, so without
// this the merchant who taps the chip, lands on /plugins and applies the
// one pending update keeps being nagged by a green "Plugin updates
// available (1)" for the rest of that interval — the chip contradicting
// the thing they just did (ut-docs#1953 review). Compare-and-swap rather
// than load-modify-store: a scheduler tick may publish a fresh count
// concurrently, and the loser of that race must not clobber the winner.
// Worst case this under-counts by one until the next tick corrects it,
// which is the right direction to be wrong in: a chip that disappears a
// little early is a far smaller sin on a till than one that won't go away.
func NotePendingUpdateApplied() {
	for {
		current := CurrentPendingUpdates()
		if current.Count <= 0 {
			return
		}
		next := PendingUpdateStatus{Count: current.Count - 1, LanguagePending: current.LanguagePending}
		if next.Count == 0 {
			next.LanguagePending = false
		}
		if pendingUpdateState.CompareAndSwap(current, next) {
			return
		}
	}
}
