package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// Order lifecycle status persistence (ut-docs#526). The status vocabulary and
// the offline multi-till conflict rule (forward-only ladder; cancelled
// terminal) live in internal/pos (order_status.go) — this file only knows how
// to store, guard and replay writes. internal/pos imports internal/data, so
// the rule reaches ApplyOrderStatus as the `allowed` callback rather than a
// direct pos import (which would be a cycle); handlers pass
// pos.OrderStatusAllowed.

// OrderStatusEvent is one row of the append-only status journal for a
// receipt: who set what, when. ActorName resolves via a LEFT JOIN on users
// (empty when the actor isn't a known user — e.g. 'system').
type OrderStatusEvent struct {
	ReceiptNo string
	Status    string
	ActorID   string
	ActorName string
	CreatedAt string
}

// OrderListEntry is one row of the recent-orders list: current status (” =
// tracking never touched this sale) plus enough context to render a line.
// The print-failed fields (ut-docs#517a) are ” unless the MOST RECENT
// kitchen/receipt print attempt failed, in which case they carry its
// RFC3339 timestamp.
type OrderListEntry struct {
	ReceiptNo            string
	OrderType            string
	Status               string
	StatusUpdatedAt      string
	CreatedAt            string
	KitchenPrintFailedAt string
	ReceiptPrintFailedAt string
}

// ApplyOrderStatus is the guarded status write: a transactional
// read-compare-write that loads the sale's current order_status, asks
// allowed(current) whether the incoming status may replace it, and only then
// updates the current-state columns AND appends the journal event — one
// transaction, so the journal can never disagree with the current state.
//
// A write allowed() rejects returns (applied=false, found=true, nil): a
// silent no-op by design — the conflict rule (pos.OrderStatusAllowed) drops
// stale offline catch-up writes without erroring and without journaling.
// The DB opens with _txlock=immediate (internal/db/db.go), so this
// transaction holds the write lock from BEGIN — a concurrent writer waits on
// busy_timeout rather than interleaving between our read and write.
func (r *POSRepo) ApplyOrderStatus(ctx context.Context, receiptNo, status, actorID, createdAt string, allowed func(current string) bool) (applied, found bool, err error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, false, fmt.Errorf("apply order status: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit

	var current string
	err = tx.QueryRowContext(ctx, `SELECT order_status FROM sales WHERE receipt_no = ?`, receiptNo).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("apply order status: read current: %w", err)
	}
	if !allowed(current) {
		return false, true, nil
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE sales SET order_status = ?, order_status_updated_at = ? WHERE receipt_no = ?
`, status, createdAt, receiptNo); err != nil {
		return false, true, fmt.Errorf("apply order status: update: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO order_status_events (id, receipt_no, status, actor_id, created_at)
VALUES (?, ?, ?, ?, ?)
`, uuid.NewString(), receiptNo, status, nullIfEmpty(actorID), createdAt); err != nil {
		return false, true, fmt.Errorf("apply order status: journal: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, true, fmt.Errorf("apply order status: commit: %w", err)
	}
	return true, true, nil
}

// SetKitchenPrintFailed records (at = RFC3339 timestamp) or clears (at = "")
// the kitchen-print failure flag for a receipt (ut-docs#517a). Called from
// both the async auto-print path and the manual reprint endpoint, so a later
// successful retry clears a previously-set flag rather than leaving it
// stuck. A single-column, single-row write with no read-compare-write (the
// order-status ladder's reason for a transaction doesn't apply here).
func (r *POSRepo) SetKitchenPrintFailed(ctx context.Context, receiptNo, at string) error {
	if _, err := r.db.ExecContext(ctx, `UPDATE sales SET kitchen_print_failed_at = ? WHERE receipt_no = ?`,
		nullIfEmpty(at), receiptNo); err != nil {
		return fmt.Errorf("set kitchen print failed: %w", err)
	}
	return nil
}

// SetReceiptPrintFailed is the same for the receipt-print path.
func (r *POSRepo) SetReceiptPrintFailed(ctx context.Context, receiptNo, at string) error {
	if _, err := r.db.ExecContext(ctx, `UPDATE sales SET receipt_print_failed_at = ? WHERE receipt_no = ?`,
		nullIfEmpty(at), receiptNo); err != nil {
		return fmt.Errorf("set receipt print failed: %w", err)
	}
	return nil
}

// ListOrderStatusEvents returns a receipt's full status history, oldest
// first (rowid breaks same-timestamp ties in insertion order).
func (r *POSRepo) ListOrderStatusEvents(ctx context.Context, receiptNo string) ([]OrderStatusEvent, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT e.receipt_no, e.status, COALESCE(e.actor_id, ''), COALESCE(u.display_name, ''), e.created_at
FROM order_status_events e
LEFT JOIN users u ON u.id = e.actor_id
WHERE e.receipt_no = ?
ORDER BY e.created_at ASC, e.rowid ASC
`, receiptNo)
	if err != nil {
		return nil, fmt.Errorf("list order status events: %w", err)
	}
	defer rows.Close()
	var out []OrderStatusEvent
	for rows.Next() {
		var ev OrderStatusEvent
		if err := rows.Scan(&ev.ReceiptNo, &ev.Status, &ev.ActorID, &ev.ActorName, &ev.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan order status event: %w", err)
		}
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list order status events: %w", err)
	}
	return out, nil
}

// LatestOrderStatus returns the newest journal event for a receipt — the
// who/when behind the current state (events append only on applied writes,
// so the latest event always matches sales.order_status). ok=false with no
// error means tracking never touched this sale.
func (r *POSRepo) LatestOrderStatus(ctx context.Context, receiptNo string) (OrderStatusEvent, bool, error) {
	var ev OrderStatusEvent
	err := r.db.QueryRowContext(ctx, `
SELECT e.receipt_no, e.status, COALESCE(e.actor_id, ''), COALESCE(u.display_name, ''), e.created_at
FROM order_status_events e
LEFT JOIN users u ON u.id = e.actor_id
WHERE e.receipt_no = ?
ORDER BY e.created_at DESC, e.rowid DESC
LIMIT 1
`, receiptNo).Scan(&ev.ReceiptNo, &ev.Status, &ev.ActorID, &ev.ActorName, &ev.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return OrderStatusEvent{}, false, nil
	}
	if err != nil {
		return OrderStatusEvent{}, false, fmt.Errorf("latest order status: %w", err)
	}
	return ev, true, nil
}

// ListRecentOrders returns the newest COMPLETED sales (returns excluded —
// the kitchen never preps a refund; voided/refunded/parked/still-open sales
// excluded too — the kitchen must never get a one-tap status control for a
// transaction that isn't a real, finished order) with their current order
// status, for the one-tap status list. Untracked sales list with Status "".
func (r *POSRepo) ListRecentOrders(ctx context.Context, limit int) ([]OrderListEntry, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT receipt_no, COALESCE(order_type, ''), order_status, COALESCE(order_status_updated_at, ''), created_at,
       COALESCE(kitchen_print_failed_at, ''), COALESCE(receipt_print_failed_at, '')
FROM sales
WHERE sale_type = 'sale' AND status = 'completed'
ORDER BY created_at DESC
LIMIT ?
`, limit)
	if err != nil {
		return nil, fmt.Errorf("list recent orders: %w", err)
	}
	defer rows.Close()
	var out []OrderListEntry
	for rows.Next() {
		var e OrderListEntry
		if err := rows.Scan(&e.ReceiptNo, &e.OrderType, &e.Status, &e.StatusUpdatedAt, &e.CreatedAt, &e.KitchenPrintFailedAt, &e.ReceiptPrintFailedAt); err != nil {
			return nil, fmt.Errorf("scan recent order: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list recent orders: %w", err)
	}
	return out, nil
}
