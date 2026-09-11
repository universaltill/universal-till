package data

// Kiosk "pay at counter" orders (universaltill/ut-docs#582): when the
// self-order kiosk is set to kiosk.payment_mode="counter", checkout never
// creates a sale — there is nothing to charge yet, the customer pays a
// human at the till after ordering. This repo is the record of THAT order:
// what to hand the customer and what the kitchen should make. It is
// DELIBERATELY NOT a sale — no money/price columns, no FK to sales (see
// 026_kiosk_counter_orders.sql's own header) — so it is invisible to
// day-close/report aggregation, exactly like a held-but-never-tendered
// sale would be if one existed for this.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// counterOrderDisplayNoPrefix is the "C-" prefix (card spec) that keeps a
// counter-order reference visually and lexically distinct from a real sale
// receipt/display number (internal/data/pos_repo.go's NextDisplayNo, which
// shares the till's sync.receipt_prefix instead) — staff must never be able
// to mistake one for the other at the counter.
const counterOrderDisplayNoPrefix = "C-"

// KioskCounterOrderStatusOpen/Collected are the only two states this table
// tracks — a plain two-state lifecycle, unlike the sales-order status
// ladder (pos.OrderStatus*), since a counter order has no kitchen-progress
// steps of its own to track here (kitchen ticket printing is fire-and-
// forget, not tracked back into this row).
const (
	KioskCounterOrderStatusOpen      = "open"
	KioskCounterOrderStatusCollected = "collected"
)

// KioskCounterOrderLine is one basket line on a counter order — just enough
// to print a kitchen ticket and show a staff-facing summary, never a price
// (this is not a sale line).
type KioskCounterOrderLine struct {
	Name string
	// Qty is pre-formatted (e.g. "2"), same convention as
	// print.KitchenItem.Qty / kitchenItemsFor's use of httpx.FormatQtyLatin
	// (internal/pages/kitchen_print.go) — this table has no numeric
	// quantity type of its own to parse back out of locale-formatted text.
	Qty       string
	Modifiers []string
}

// KioskCounterOrder is one "pay at counter" kiosk order.
type KioskCounterOrder struct {
	ID          string
	DisplayNo   string
	OrderType   string
	Status      string
	CreatedAt   string
	CollectedAt string
	Lines       []KioskCounterOrderLine
}

type KioskCounterOrdersRepo struct {
	db *sql.DB
}

func NewKioskCounterOrdersRepo(db *sql.DB) *KioskCounterOrdersRepo {
	return &KioskCounterOrdersRepo{db: db}
}

// Create inserts a new open counter order and returns it with ID and
// DisplayNo filled in — order.ID, if empty, is generated; order.DisplayNo
// is always (re)generated here — the caller never supplies one — as the
// next free "C-"-prefixed reference, read and inserted inside the same
// transaction to keep the race window as small as the equivalent
// NextDisplayNo/NextReceiptNo pattern (internal/data/pos_repo.go) accepts
// elsewhere: a crash between read and insert leaves a gap, never a
// collision that corrupts data — display_no carries no unique constraint
// (mirrors sales.display_no, 014_sale_display_no.sql), so even a genuine
// race only costs a duplicated-looking label, never a lost or broken row.
// order.Status/CreatedAt are set here, not trusted from the caller, so
// every row this method creates starts open and honestly timed. Returns
// the filled-in order (rather than just an error) because the caller — the
// counter-mode checkout handler — needs the generated DisplayNo to render
// the confirmation screen; re-querying it back out would be pure overhead.
func (r *KioskCounterOrdersRepo) Create(ctx context.Context, order KioskCounterOrder) (KioskCounterOrder, error) {
	id := strings.TrimSpace(order.ID)
	if id == "" {
		id = uuid.NewString()
	}
	linesJSON, err := json.Marshal(order.Lines)
	if err != nil {
		return KioskCounterOrder{}, fmt.Errorf("marshal counter order lines: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return KioskCounterOrder{}, fmt.Errorf("begin counter order create: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once Commit succeeds

	var maxVal sql.NullInt64
	if err := tx.QueryRowContext(ctx, `
SELECT COALESCE(MAX(CAST(substr(display_no, ?) AS INTEGER)), 0)
FROM kiosk_counter_orders WHERE display_no LIKE ? || '%'`,
		len(counterOrderDisplayNoPrefix)+1, counterOrderDisplayNoPrefix).Scan(&maxVal); err != nil {
		return KioskCounterOrder{}, fmt.Errorf("next counter order display no: %w", err)
	}
	next := maxVal.Int64 + 1
	if next < 1 {
		next = 1
	}
	displayNo := counterOrderDisplayNoPrefix + strconv.FormatInt(next, 10)

	if _, err := tx.ExecContext(ctx, `
INSERT INTO kiosk_counter_orders (id, display_no, order_type, lines_json, status, created_at)
VALUES (?, ?, ?, ?, ?, ?)`,
		id, displayNo, order.OrderType, string(linesJSON), KioskCounterOrderStatusOpen, now); err != nil {
		return KioskCounterOrder{}, fmt.Errorf("insert counter order: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return KioskCounterOrder{}, fmt.Errorf("commit counter order create: %w", err)
	}
	order.ID = id
	order.DisplayNo = displayNo
	order.Status = KioskCounterOrderStatusOpen
	order.CreatedAt = now
	return order, nil
}

// ListOpen returns every open counter order, oldest first — the natural
// "what's still waiting" queue order for the staff-facing board.
func (r *KioskCounterOrdersRepo) ListOpen(ctx context.Context) ([]KioskCounterOrder, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, display_no, order_type, lines_json, status, created_at, COALESCE(collected_at, '')
FROM kiosk_counter_orders WHERE status = ? ORDER BY created_at ASC`, KioskCounterOrderStatusOpen)
	if err != nil {
		return nil, fmt.Errorf("list open counter orders: %w", err)
	}
	defer rows.Close()

	var out []KioskCounterOrder
	for rows.Next() {
		var o KioskCounterOrder
		var linesJSON string
		if err := rows.Scan(&o.ID, &o.DisplayNo, &o.OrderType, &linesJSON, &o.Status, &o.CreatedAt, &o.CollectedAt); err != nil {
			return nil, fmt.Errorf("scan counter order: %w", err)
		}
		if linesJSON != "" {
			if err := json.Unmarshal([]byte(linesJSON), &o.Lines); err != nil {
				return nil, fmt.Errorf("unmarshal counter order lines: %w", err)
			}
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate open counter orders: %w", err)
	}
	return out, nil
}

// MarkCollected moves id to the terminal 'collected' state and stamps
// collected_at. A missing/already-collected id is a silent no-op (0 rows
// affected) — the caller (the mark-collected API) treats "not found" and
// "already collected" the same way a re-tapped button should: nothing
// left to do, not an error.
//
// The `status = open` clause is what actually makes the "already
// collected" half of that true (review finding, ut-docs#582): the board
// polls every 15s, so two tills can both be showing the same open row,
// and without it the second tap would overwrite the FIRST tap's
// collected_at with a later time — quietly rewriting when the customer
// actually took their order. Collection time is the only timestamp this
// row carries beyond created_at; it should record the first collection,
// not the last tap.
func (r *KioskCounterOrdersRepo) MarkCollected(ctx context.Context, id string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := r.db.ExecContext(ctx, `
UPDATE kiosk_counter_orders SET status = ?, collected_at = ? WHERE id = ? AND status = ?`,
		KioskCounterOrderStatusCollected, now, id, KioskCounterOrderStatusOpen); err != nil {
		return fmt.Errorf("mark counter order collected: %w", err)
	}
	return nil
}
