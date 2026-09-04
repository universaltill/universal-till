package pos

import "sync"

// Order lifecycle status (ut-docs#526) — the ONE central model the Kitchen
// Display (#516), pagers (#517) and customer tracking (#528/#527) will all
// key off later. It lives here in internal/pos, mirroring how
// OrderTypeTakeaway is this package's shared order-type convention: core owns
// the vocabulary, surfaces just render it.
//
// "" (empty) is deliberately NOT a status: it means "no kitchen-status
// tracking has touched this sale" — existing sales and shops that never use
// the feature stay untouched.
const (
	OrderStatusNew       = "new"
	OrderStatusPreparing = "preparing"
	OrderStatusReady     = "ready"
	OrderStatusCollected = "collected"
	// OrderStatusCancelled is terminal and off the forward ladder: reachable
	// from any state except collected (and itself), and nothing leaves it.
	OrderStatusCancelled = "cancelled"
)

// OrderStatuses is the fixed vocabulary, in lifecycle order.
var OrderStatuses = []string{OrderStatusNew, OrderStatusPreparing, OrderStatusReady, OrderStatusCollected, OrderStatusCancelled}

// orderStatusRank is the forward ladder new(1) < preparing(2) < ready(3) <
// collected(4). "" and cancelled rank 0: neither sits on the ladder — the
// cancel rule is handled explicitly in OrderStatusAllowed, never by rank
// comparison.
var orderStatusRank = map[string]int{
	OrderStatusNew:       1,
	OrderStatusPreparing: 2,
	OrderStatusReady:     3,
	OrderStatusCollected: 4,
}

// OrderStatusRank returns a status's position on the forward ladder
// (0 for "", cancelled, or anything unknown).
func OrderStatusRank(status string) int {
	return orderStatusRank[status]
}

// ValidOrderStatus reports whether s is one of the five defined statuses.
// "" is NOT valid input for a status write — it is only ever the initial
// untracked state.
func ValidOrderStatus(s string) bool {
	if s == OrderStatusCancelled {
		return true
	}
	return orderStatusRank[s] > 0
}

// IsTerminalOrderStatus reports whether s is a terminal lifecycle state
// (collected or cancelled) — an order in either state is done and leaves
// the active /orders queue (ut-docs#1389), while still keeping its sale,
// audit trail and journal/reporting history untouched elsewhere.
func IsTerminalOrderStatus(s string) bool {
	return s == OrderStatusCollected || s == OrderStatusCancelled
}

// OrderStatusAllowed is THE offline multi-till conflict rule for order
// status (ut-docs#526, extending ADR-0011's fixed-and-simple conflict-rule
// philosophy — decided by the Architect step, do not relitigate here):
//
//   - a write applies only if it moves the status FORWARD on the ladder
//     new < preparing < ready < collected (so a stale "preparing" arriving
//     after another till already recorded "ready" is dropped);
//   - OR the write is "cancelled" and the current status is not already
//     collected or cancelled;
//   - a dropped write is a silent no-op, never an error — offline sync
//     catch-up is not a user-facing failure, and the visible status must
//     never regress.
//
// current is the sale's stored status ("" = untracked); next is the incoming
// write. Callers only append a journal event / broadcast when this returned
// true and the guarded write actually applied.
func OrderStatusAllowed(current, next string) bool {
	if current == OrderStatusCancelled {
		return false // terminal — nothing leaves cancelled
	}
	if next == OrderStatusCancelled {
		return current != OrderStatusCollected
	}
	nextRank := orderStatusRank[next]
	if nextRank == 0 {
		return false // unknown / "" — never a legal write
	}
	return orderStatusRank[current] < nextRank
}

// OrderStatusChanged is the event published on every APPLIED status write —
// dropped (stale) writes never publish. At is RFC3339 UTC, same format the
// journal row persists.
//
// Also a wire type since ADR-0079 (ut-docs#1571): the SSE stream endpoints
// (/api/orders/stream, /api/sync/orders/stream) JSON-encode it verbatim and
// the replica bridge decodes it back — hence the snake_case tags, per the
// product-wide JSON convention.
type OrderStatusChanged struct {
	ReceiptNo string `json:"receipt_no"`
	Status    string `json:"status"`
	ActorID   string `json:"actor_id"`
	At        string `json:"at"`
}

// orderStatusSubBuffer is each subscriber's channel capacity. Small on
// purpose: this is a "something changed, go re-read" nudge, not a
// delivery-guaranteed queue — a subscriber that falls this far behind
// re-reads current state from the repo anyway.
const orderStatusSubBuffer = 16

// OrderStatusBroadcaster is the in-process pub/sub for order status changes
// (ut-docs#526 item 5): the future KDS/pager/customer-tracking surfaces
// (#516/#517/#528/#527) Subscribe instead of polling the table. One instance
// lives on common.Deps for the process lifetime.
//
// Publish is non-blocking by contract: a slow or absent subscriber must
// never block the publisher (a status tap on the shop floor) — when a
// subscriber's buffer is full the event is dropped for that subscriber only.
type OrderStatusBroadcaster struct {
	mu     sync.Mutex
	next   int
	subs   map[int]chan OrderStatusChanged
	closed bool
}

func NewOrderStatusBroadcaster() *OrderStatusBroadcaster {
	return &OrderStatusBroadcaster{subs: map[int]chan OrderStatusChanged{}}
}

// Subscribe registers a new subscriber and returns its receive channel plus
// an idempotent cancel func. cancel closes the channel (under the same lock
// Publish sends under, so there is no send-on-closed race).
//
// After Close, Subscribe hands back an already-closed channel and registers
// nothing: a stream handler that races process shutdown sees "closed" on
// its first receive and returns at once instead of waiting forever.
func (b *OrderStatusBroadcaster) Subscribe() (<-chan OrderStatusChanged, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := make(chan OrderStatusChanged, orderStatusSubBuffer)
	if b.closed {
		close(ch)
		return ch, func() {}
	}
	id := b.next
	b.next++
	b.subs[id] = ch
	cancel := func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if c, ok := b.subs[id]; ok {
			delete(b.subs, id)
			close(c)
		}
	}
	return ch, cancel
}

// Publish delivers ev to every subscriber whose buffer has room and drops it
// for any that are full. Never blocks. A no-op after Close (the subscriber
// map is empty by then).
func (b *OrderStatusBroadcaster) Publish(ev OrderStatusChanged) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.subs {
		select {
		case ch <- ev:
		default: // full buffer — drop for this subscriber, latest-state semantics
		}
	}
}

// SubscriberCount reports how many live subscriptions exist. Diagnostic —
// the SSE handler tests use it to prove unsubscribe-on-disconnect actually
// runs (ADR-0079); nothing on the hot path should branch on it.
func (b *OrderStatusBroadcaster) SubscriberCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}

// Close releases every live subscriber (closing their channels, under the
// same lock Publish sends under — no send-on-closed race) and makes the
// broadcaster inert for the rest of the process lifetime. Wired to app.Run's
// bgCtx in pages.Init (ADR-0079): the SSE stream handlers block on their
// subscriber channel between events, and a replica's bridge holds one such
// stream open on its primary indefinitely — without this, every primary
// shutdown (a self-update restart included) would sit out
// http.Server.Shutdown's full timeout waiting on a connection that was never
// going to end on its own. Idempotent.
func (b *OrderStatusBroadcaster) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	for id, ch := range b.subs {
		delete(b.subs, id)
		close(ch)
	}
}
