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
}

var pendingUpdateState atomic.Value // PendingUpdateStatus

// CurrentPendingUpdates returns the last-known pending-update count (the
// zero value, Count 0, before the background scheduler's first tick).
func CurrentPendingUpdates() PendingUpdateStatus {
	if s, ok := pendingUpdateState.Load().(PendingUpdateStatus); ok {
		return s
	}
	return PendingUpdateStatus{}
}

// SetPendingUpdates records the outcome of one scheduler tick. Exported so
// internal/pages' StartPluginUpdateScheduler (which owns the DB/catalog
// access needed to actually run the check) can publish the result here for
// the status-chip template funcs to read.
func SetPendingUpdates(count int) {
	pendingUpdateState.Store(PendingUpdateStatus{Count: count})
}
