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
	// Qty is the RAW quantity, deliberately not pre-formatted (ut-docs#2221)
	// — this one row feeds two destinations with opposite digit-shape needs:
	// printCounterOrderTicketAsync's kitchen ticket (self_order_shop.go),
	// which must stay Latin (an ESC/POS printer can't render Arabic-Indic
	// glyphs, same reasoning as print.KitchenItem.Qty elsewhere), and
	// counterOrderItemsSummary's on-screen staff board
	// (kiosk_counter_orders_page.go), which should follow the viewing
	// operator's locale like every other on-screen quantity. A single
	// pre-formatted string could only ever be right for one of the two.
	Qty       float64
	Modifiers []string
}

// UnmarshalJSON accepts Qty as either a JSON number (the current shape) or
// a JSON string (every row this table held before ut-docs#2221 switched
// Qty from a pre-formatted string to a raw number) — so a row an
// already-open counter order left behind at upgrade time still reads back
// instead of taking down ListOpen, and with it the whole staff "pay at
// counter" board, on its very next poll. A legacy string was produced by
// FormatQtyLatin(qty, locale) at write time: never digit-shaped, but its
// decimal separator followed the KIOSK CUSTOMER's locale at write time
// (de/tr use "," — see numberSeparators), which isn't itself stored on the
// row. Since a counter-order quantity realistically never reaches the
// low thousands, thousands-grouping is a non-issue in practice; the only
// real ambiguity is the decimal mark, so this tries a plain parse first
// (covers every locale that already uses ".") and retries with "," folded
// to "." otherwise (covers de/tr) — best-effort, not a claim of parsing
// every locale's number format in general.
func (l *KioskCounterOrderLine) UnmarshalJSON(b []byte) error {
	type alias KioskCounterOrderLine
	aux := &struct {
		Qty json.RawMessage
		*alias
	}{alias: (*alias)(l)}
	if err := json.Unmarshal(b, aux); err != nil {
		return err
	}
	raw := strings.TrimSpace(string(aux.Qty))
	if raw == "" || raw == "null" {
		return nil
	}
	if raw[0] != '"' {
		var q float64
		if err := json.Unmarshal(aux.Qty, &q); err != nil {
			return fmt.Errorf("unmarshal counter order line qty %s: %w", raw, err)
		}
		l.Qty = q
		return nil
	}
	var s string
	if err := json.Unmarshal(aux.Qty, &s); err != nil {
		return fmt.Errorf("unmarshal legacy string counter order line qty %s: %w", raw, err)
	}
	s = strings.TrimSpace(s)
	q, err := strconv.ParseFloat(s, 64)
	if err != nil {
		q, err = strconv.ParseFloat(strings.ReplaceAll(s, ",", "."), 64)
	}
	if err != nil {
		return fmt.Errorf("parse legacy string counter order line qty %q: %w", s, err)
	}
	l.Qty = q
	return nil
}

// KioskCounterOrder is one "pay at counter" kiosk order.
type KioskCounterOrder struct {
	ID          string
	DisplayNo   string
	OrderType   string
	Status      string
	CreatedAt   string
	CollectedAt string
	// TableID/TableLabel (ut-docs#815, migration 029) are the physical
	// table this order was placed from, via /self-order?table=<id> on the
	// guest's own phone — both empty for a plain kiosk-till counter order
	// (today's #582 flow, completely unaffected). TableID is the only
	// field Create persists (table_id); TableLabel is never a column —
	// Create returns it unchanged from what the caller (the checkout
	// handler, which already resolved it via SetTable) passed in, while
	// ListOpen resolves it fresh via a LEFT JOIN tables, the same
	// GetSaleDetail/TableLabel convention #820 already established for a
	// real sale. That split matters: a table can be renamed/deactivated
	// after an order was placed, and the staff board should show what the
	// table is called NOW, not a stale label frozen at order time.
	TableID    string
	TableLabel string
	Lines      []KioskCounterOrderLine
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
INSERT INTO kiosk_counter_orders (id, display_no, order_type, lines_json, status, created_at, table_id)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, displayNo, order.OrderType, string(linesJSON), KioskCounterOrderStatusOpen, now, nullIfEmpty(order.TableID)); err != nil {
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
	// LEFT JOIN tables (ut-docs#815): resolves TableLabel fresh at read
	// time, same convention as pos_repo.go's GetSaleDetail — a table can be
	// renamed after the order was placed, and the board should show its
	// current name. COALESCE(t.label, '') keeps a NULL table_id (the
	// common, no-table case) and a table_id whose row is somehow gone both
	// reading as "" rather than NULL.
	rows, err := r.db.QueryContext(ctx, `
SELECT k.id, k.display_no, k.order_type, k.lines_json, k.status, k.created_at, COALESCE(k.collected_at, ''),
       COALESCE(k.table_id, ''), COALESCE(t.label, '')
FROM kiosk_counter_orders k LEFT JOIN tables t ON t.id = k.table_id
WHERE k.status = ? ORDER BY k.created_at ASC`, KioskCounterOrderStatusOpen)
	if err != nil {
		return nil, fmt.Errorf("list open counter orders: %w", err)
	}
	defer rows.Close()

	var out []KioskCounterOrder
	for rows.Next() {
		var o KioskCounterOrder
		var linesJSON string
		if err := rows.Scan(&o.ID, &o.DisplayNo, &o.OrderType, &linesJSON, &o.Status, &o.CreatedAt, &o.CollectedAt, &o.TableID, &o.TableLabel); err != nil {
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
