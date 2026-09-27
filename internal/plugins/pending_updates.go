package plugins

import "sync/atomic"

// CanonicalTypeLanguage is the "language" canonical type (ADR-0002's plugin
// taxonomy) — the one type StartPluginUpdateScheduler's tick auto-applies
// without asking (ut-docs#1953) and common.Deps.RefreshPendingUpdates
// (ut-docs#2787) excludes from its main-till count for the same reason, so
// both packages key off one shared constant instead of two string literals
// that could drift.
const CanonicalTypeLanguage = "language"

// PendingUpdateStatus is the last-known count of installed-plugin updates
// still waiting on a merchant decision — i.e. every update the last
// StartPluginUpdateScheduler tick or common.Deps.RefreshPendingUpdates call
// found MINUS any the scheduler auto-applied itself (a language pack is
// content, not code, and auto-applies silently; see ut-docs#1953).
// RefreshPendingUpdates (ut-docs#2787) republishes this right after every
// plugin lifecycle change, so it's usually fresher than the scheduler's own
// tick. Mirrors internal/updates.Status's atomic-value pattern so the till
// can show a status-bar chip without a per-request DB query.
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
	// MainTillURL is set on a joined till (ut-docs#2783): the address of the
	// main till whose plugin versions this till follows (ut-docs#460). A
	// joined till never installs an update itself, so its status-bar chip
	// says updates are installed from the main till and points there,
	// instead of offering updates the owner can't act on. Empty on a main or
	// standalone till.
	MainTillURL string
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

// PublishPendingUpdates records the outcome of one scheduler tick or refresh.
// Exported so internal/pages' StartPluginUpdateScheduler and
// internal/pages/common's RefreshPendingUpdates (ut-docs#2787) — both of
// which own the DB/catalog access needed to actually run the check — can
// publish the result here for the status-chip template funcs to read. A
// joined till publishes its MainTillURL alongside the count (ut-docs#2783;
// this replaced SetPendingUpdates(count, languagePending)). A negative count
// is clamped to zero, and LanguagePending is forced false when nothing is
// pending, so the fields can never disagree about there being anything
// pending at all.
func PublishPendingUpdates(s PendingUpdateStatus) {
	if s.Count < 0 {
		s.Count = 0
	}
	if s.Count == 0 {
		s.LanguagePending = false
	}
	pendingUpdateState.Store(s)
}
