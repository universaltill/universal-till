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

// OrderTrackingUntrackedExpiry (ut-docs#908) is how long a tracking link to
// an UNTRACKED order (order_status "", nobody has moved it on the Orders
// screen) keeps answering after the sale was created. Counter receipts carry
// a tracking QR, so most tokened sales are never tracked at all; without
// this bound every one of them would stay "live" forever — answering on
// /o/{token} and riding every cloud relay push indefinitely. 12h covers any
// same-day collection while keeping the pushed set to roughly one day's
// sales.
const OrderTrackingUntrackedExpiry = 12 * time.Hour

// OrderTrackingVisible reports whether a tracking token should still resolve.
// Three cases, each keyed off the timestamp that matters for it (all RFC3339;
// createdAt also accepts SQLite's datetime('now') shape, the sales column's
// default):
//   - a TRACKED live order (new/preparing/ready) is always visible, no
//     matter how old — staff are working it, the customer is waiting;
//   - a terminal order expires OrderTrackingExpiry after its last status
//     write (statusUpdatedAt);
//   - an UNTRACKED order ("") expires OrderTrackingUntrackedExpiry after the
//     sale was created (createdAt) — ut-docs#908.
//
// An unparseable timestamp the rule needs fails closed — on an anonymous
// surface a broken row must read as "gone", not "visible forever".
func OrderTrackingVisible(status, statusUpdatedAt, createdAt string, now time.Time) bool {
	if status == "" {
		at, ok := parseOrderTrackingTime(createdAt)
		if !ok {
			return false
		}
		return now.Sub(at) <= OrderTrackingUntrackedExpiry
	}
	if !OrderTrackingTerminal(status) {
		return true
	}
	at, err := time.Parse(time.RFC3339, statusUpdatedAt)
	if err != nil {
		return false
	}
	return now.Sub(at) <= OrderTrackingExpiry
}

// parseOrderTrackingTime reads a sales.created_at value: RFC3339 (what every
// sale writer stores) or SQLite's "YYYY-MM-DD HH:MM:SS" UTC (the column's
// datetime('now') default). data.ListLiveTrackedOrders' SQL pre-prune
// compares the same column through julianday(), which accepts exactly these
// shapes too, so the two never disagree on a row this keeps.
func parseOrderTrackingTime(v string) (time.Time, bool) {
	if at, err := time.Parse(time.RFC3339, v); err == nil {
		return at, true
	}
	if at, err := time.Parse("2006-01-02 15:04:05", v); err == nil {
		return at, true
	}
	return time.Time{}, false
}
