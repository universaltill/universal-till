package pos

import "time"

// Customer order-tracking liveness (ut-docs#527, ADR-0070). The rule lived in
// internal/pages/order_tracking.go until the cloud relay push (ut-docs#907)
// needed it too: internal/cloudsync cannot import internal/pages (pages wires
// cloudsync — a cycle), so the ONE liveness rule moved here, next to the
// status vocabulary it keys off. internal/pages delegates to these; keep any
// change here in lockstep with ut-cloud's read endpoint, which mirrors this
// rule by necessity (see ut-cloud internal/httpapi/handlers/stores.go).

// OrderTrackingExpiry is how long a tracking link keeps answering after the
// order reaches a terminal status. Computed in Go from the timestamp the
// lookup already returns — no cron, no extra writes, nothing stored.
const OrderTrackingExpiry = 2 * time.Hour

// OrderTrackingTerminal reports whether status ends the order's lifecycle —
// the polling stop condition and the start of the expiry window.
func OrderTrackingTerminal(status string) bool {
	return status == OrderStatusCollected || status == OrderStatusCancelled
}

// OrderTrackingTerminalStatuses is OrderTrackingTerminal's status set as a
// slice, for a caller (data.ListLiveTrackedOrders via cloudsync.go,
// ut-docs#1321) that needs to pass it across the internal/data/internal/pos
// import-cycle boundary this file's own header explains — a SQL `NOT IN
// (...)` needs the literal values, not just the predicate function. Kept as
// a single source alongside OrderTrackingTerminal so the two can never
// drift out of lockstep.
func OrderTrackingTerminalStatuses() []string {
	return []string{OrderStatusCollected, OrderStatusCancelled}
}

// OrderTrackingVisible reports whether a tracking token should still resolve.
// A live order (anything non-terminal, including the untracked "") is always
// visible; a terminal one expires OrderTrackingExpiry after its last status
// write (statusUpdatedAt, RFC3339). An unparseable terminal timestamp fails
// closed — on an anonymous surface a broken row must read as "gone", not
// "visible forever".
func OrderTrackingVisible(status, statusUpdatedAt string, now time.Time) bool {
	if !OrderTrackingTerminal(status) {
		return true
	}
	at, err := time.Parse(time.RFC3339, statusUpdatedAt)
	if err != nil {
		return false
	}
	return now.Sub(at) <= OrderTrackingExpiry
}
