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
//
// ut-docs#2703: since then the payable object is a HELD SALE. A
// pay-at-counter checkout parks the kiosk basket in held_sales (listed on
// Open orders, paid through the normal tender -- a real signed sale) and
// writes its row here with status "held" (Create), to allocate the
// "C-" number and record what was ordered. Rows in "open" are orders
// placed before that change (ListOpen); since the reopened #2703 they are
// listed under Open orders' "Pay at the counter" tab and become payable held
// sales when opened (ConvertOpenToHeld).

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

// counterOrderHeldRetention (ut-docs#2714) is how long a "held" row is kept.
// A held row is only the C-number record of an order whose payable object
// is the held sale; once the order is paid or abandoned it serves no one,
// and nothing else ever removes it. 90 days comfortably outlives any real
// parked order (and any "what number did I have?" question) while keeping
// the table bounded. Create prunes past it; open/collected rows (the
// legacy staff-board lifecycle) are never pruned.
const counterOrderHeldRetention = 90 * 24 * time.Hour

// counterOrderTillIDPrefixLen (ut-docs#2714) is how many alphanumeric
// characters of sync.till_id a replica with no sync.receipt_prefix uses to
// namespace its C-numbers -- short enough to call out at the counter, long
// enough that two replicas' generated ids practically never collide.
const counterOrderTillIDPrefixLen = 6

// KioskCounterOrderStatusOpen/Held are the states this table's code still
// writes -- a plain lifecycle, unlike the sales-order status ladder
// (pos.OrderStatus*), since a counter order has no kitchen-progress steps of
// its own to track here (kitchen ticket printing is fire-and-forget, not
// tracked back into this row). Rows may also carry the historical value
// "collected" (the removed "Mark collected" action, which closed an order
// unpaid; ut-docs#2703 reopened): nothing writes it any more, ListOpen
// (status = open) and the held-row prune (status = held) skip such rows,
// and Get returns them as-is -- they load fine and are simply never acted
// on.
const (
	KioskCounterOrderStatusOpen = "open"
	// KioskCounterOrderStatusHeld (ut-docs#2703): since the product owner's
	// "it should be exactly the same as a hold order" decision, a
	// pay-at-counter checkout parks the kiosk basket as a held sale -- the
	// payable object, listed on Open orders and paid through the normal
	// tender -- and this row is only the record of the order number it was
	// given (the "C-" sequence lives in this table) and of what was ordered.
	// A "held" row is never listed on the legacy staff board (ListOpen) and
	// never collectable. Since the reopened ut-docs#2703 nothing marks any
	// row "collected" (that closed an order unpaid); ConvertOpenToHeld
	// moves a legacy "open" row here when the cashier opens it.
	KioskCounterOrderStatusHeld = "held"
)

// KioskCounterOrderLine is one basket line on a counter order — just enough
// to print a kitchen ticket and show a staff-facing summary, never a price
// (this is not a sale line).
type KioskCounterOrderLine struct {
	Name string
	// Qty is the RAW quantity, deliberately not pre-formatted (ut-docs#2221)
	// — this one row feeds two destinations with opposite digit-shape needs:
	// printCounterOrderTicket's kitchen ticket (self_order_shop.go),
	// which must stay Latin (an ESC/POS printer can't render Arabic-Indic
	// glyphs, same reasoning as print.KitchenItem.Qty elsewhere), and
	// counterOrderItemsSummary's on-screen list on Open orders'
	// pay-at-the-counter tab (open_orders_counter.go), which should follow the viewing
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
	ID        string
	DisplayNo string
	OrderType string
	Status    string
	CreatedAt string
	// CollectedAt is set only on historical "collected" rows (see the
	// status constants); nothing writes it any more.
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

// Create inserts a new counter order and returns it with ID and DisplayNo
// filled in — order.ID, if empty, is generated; order.DisplayNo is always
// (re)generated here — the caller never supplies one — as the next free
// "C-"-prefixed reference, read and inserted inside ONE transaction. The DSN
// opens every tx with BEGIN IMMEDIATE (internal/db/db.go, _txlock), so the
// read-MAX + insert is serialised against every other writer on this till,
// and migration 044's unique index on display_no (ut-docs#2714) turns any
// remaining collision into a failed insert rather than two customers holding
// the same number. order.Status/CreatedAt are set here, not trusted from the
// caller, so every row starts open (or "held", the one status a caller may
// ask for, ut-docs#2703) and honestly timed. In the same tx, after the
// insert, "held" rows older than counterOrderHeldRetention are pruned
// (ut-docs#2714) — the new row already holds the max, so the sequence never
// goes down. Returns the filled-in order because the caller — the
// counter-mode checkout handler — needs the generated DisplayNo to render
// the confirmation screen.
func (r *KioskCounterOrdersRepo) Create(ctx context.Context, order KioskCounterOrder) (KioskCounterOrder, error) {
	// ut-docs#2703: the one caller-chosen status is "held" -- a
	// pay-at-counter order whose payable basket was parked as a held sale
	// (completeCounterOrderCheckout): same "C-" number allocation, but the
	// legacy open list never shows it and nothing can close it without
	// payment. Anything else starts "open", as before.
	status := KioskCounterOrderStatusOpen
	if order.Status == KioskCounterOrderStatusHeld {
		status = KioskCounterOrderStatusHeld
	}
	id := strings.TrimSpace(order.ID)
	if id == "" {
		id = uuid.NewString()
	}
	linesJSON, err := json.Marshal(order.Lines)
	if err != nil {
		return KioskCounterOrder{}, fmt.Errorf("marshal counter order lines: %w", err)
	}
	nowT := time.Now().UTC()
	now := nowT.Format(time.RFC3339)

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return KioskCounterOrder{}, fmt.Errorf("begin counter order create: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once Commit succeeds

	// ut-docs#2703 (review F2): each till mints this sequence from its OWN
	// table, and a held counter order is pushed to the main till -- so two
	// kiosks on two tills both minted "C-1" onto the main's Open orders and
	// into sales.display_no. The till's sync.receipt_prefix namespaces it,
	// read exactly as POSRepo.NextDisplayNo reads it (in the same tx, a
	// missing row meaning no prefix): "C-<prefix><n>", or the plain "C-<n>"
	// on the main till with no prefix. ut-docs#2714: a REPLICA
	// (sync.primary_url set) with a blank prefix would still collide with
	// the main till, so it derives one from sync.till_id instead
	// (counterOrderPrefixFromTillID); with no till id either it stays plain.
	// NextDisplayNo still never counts these: a "C-..." value either fails
	// its LIKE '<prefix>%' or CASTs to 0.
	var tillPrefix, primaryURL, tillID string
	_ = tx.QueryRowContext(ctx,
		`SELECT value FROM settings WHERE key = 'sync.receipt_prefix'`).Scan(&tillPrefix)
	if strings.TrimSpace(tillPrefix) == "" {
		_ = tx.QueryRowContext(ctx,
			`SELECT value FROM settings WHERE key = 'sync.primary_url'`).Scan(&primaryURL)
		if strings.TrimSpace(primaryURL) != "" {
			_ = tx.QueryRowContext(ctx,
				`SELECT value FROM settings WHERE key = 'sync.till_id'`).Scan(&tillID)
			tillPrefix = counterOrderPrefixFromTillID(tillID)
		}
	}
	seqPrefix := counterOrderDisplayNoPrefix + tillPrefix
	var maxVal sql.NullInt64
	if err := tx.QueryRowContext(ctx, `
SELECT COALESCE(MAX(CAST(substr(display_no, ?) AS INTEGER)), 0)
FROM kiosk_counter_orders WHERE display_no LIKE ? || '%'`,
		len(seqPrefix)+1, seqPrefix).Scan(&maxVal); err != nil {
		return KioskCounterOrder{}, fmt.Errorf("next counter order display no: %w", err)
	}
	next := maxVal.Int64 + 1
	if next < 1 {
		next = 1
	}
	displayNo := seqPrefix + strconv.FormatInt(next, 10)

	if _, err := tx.ExecContext(ctx, `
INSERT INTO kiosk_counter_orders (id, display_no, order_type, lines_json, status, created_at, table_id)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, displayNo, order.OrderType, string(linesJSON), status, now, nullIfEmpty(order.TableID)); err != nil {
		return KioskCounterOrder{}, fmt.Errorf("insert counter order: %w", err)
	}
	cutoff := nowT.Add(-counterOrderHeldRetention).Format(time.RFC3339)
	if _, err := tx.ExecContext(ctx, `
DELETE FROM kiosk_counter_orders WHERE status = ? AND created_at < ?`,
		KioskCounterOrderStatusHeld, cutoff); err != nil {
		return KioskCounterOrder{}, fmt.Errorf("prune stale held counter orders: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return KioskCounterOrder{}, fmt.Errorf("commit counter order create: %w", err)
	}
	order.ID = id
	order.DisplayNo = displayNo
	order.Status = status
	order.CreatedAt = now
	return order, nil
}

// counterOrderPrefixFromTillID derives a replica's C-number namespace from
// its sync.till_id (ut-docs#2714): the first counterOrderTillIDPrefixLen
// ASCII letters/digits, upper-cased, plus "-" ("7b-2e_91ff…" -> "7B2E91-").
// "" when the id has none, which keeps the plain "C-<n>".
func counterOrderPrefixFromTillID(tillID string) string {
	var b strings.Builder
	for _, c := range tillID {
		if b.Len() == counterOrderTillIDPrefixLen {
			break
		}
		switch {
		case c >= '0' && c <= '9', c >= 'A' && c <= 'Z':
			b.WriteRune(c)
		case c >= 'a' && c <= 'z':
			b.WriteRune(c - 'a' + 'A')
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return b.String() + "-"
}

// DeleteHeld removes the "held" counter row id (ut-docs#2714): the
// pay-at-counter checkout calls it when parking the order as a held sale
// failed, so no orphan row is left that nothing lists or ever cleans up —
// and since the row held the max, the retry reuses the same C-number. Only
// a "held" row is ever deleted (an open/collected row is real history); an
// unknown id is a silent no-op.
func (r *KioskCounterOrdersRepo) DeleteHeld(ctx context.Context, id string) error {
	if _, err := r.db.ExecContext(ctx, `
DELETE FROM kiosk_counter_orders WHERE id = ? AND status = ?`, id, KioskCounterOrderStatusHeld); err != nil {
		return fmt.Errorf("delete held counter order: %w", err)
	}
	return nil
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

// Get returns one counter order by id, any status, with its table label
// resolved fresh (same LEFT JOIN as ListOpen). found=false for an unknown id.
func (r *KioskCounterOrdersRepo) Get(ctx context.Context, id string) (KioskCounterOrder, bool, error) {
	var o KioskCounterOrder
	var linesJSON string
	err := r.db.QueryRowContext(ctx, `
SELECT k.id, k.display_no, k.order_type, k.lines_json, k.status, k.created_at, COALESCE(k.collected_at, ''),
       COALESCE(k.table_id, ''), COALESCE(t.label, '')
FROM kiosk_counter_orders k LEFT JOIN tables t ON t.id = k.table_id
WHERE k.id = ?`, id).Scan(&o.ID, &o.DisplayNo, &o.OrderType, &linesJSON, &o.Status, &o.CreatedAt, &o.CollectedAt, &o.TableID, &o.TableLabel)
	if errors.Is(err, sql.ErrNoRows) {
		return KioskCounterOrder{}, false, nil
	}
	if err != nil {
		return KioskCounterOrder{}, false, fmt.Errorf("get counter order: %w", err)
	}
	if linesJSON != "" {
		if err := json.Unmarshal([]byte(linesJSON), &o.Lines); err != nil {
			return KioskCounterOrder{}, false, fmt.Errorf("unmarshal counter order lines: %w", err)
		}
	}
	return o, true, nil
}

// ConvertOpenToHeld (ut-docs#2703, reopened) turns a legacy "open" counter
// order -- placed before pay-at-counter orders were parked as held sales,
// and until now closable only unpaid via "Mark collected" -- into a held
// sale the cashier can resume and take payment for. h is the held sale the
// caller priced from the current catalog; h.ID must be the counter row's
// own id, so the order keeps one identity (the same id-sharing the
// pay-at-counter checkout uses; nothing in the held-sale code depends on the
// "hold-" prefix).
//
// One transaction (BEGIN IMMEDIATE, internal/db/db.go): the row moves
// open -> held only if it is still open, and the held_sales row is written
// only when that move happened. So two taps on this till's database -- a
// double tap, or two sessions -- produce exactly one held sale: the loser
// gets converted=false and a nil error, and simply resumes the order the
// winner parked. A failure rolls both back, leaving the order open and
// listed. The held_sales row is written directly (primary_synced=0, not
// via the replica write-through): a legacy kiosk row only ever existed on
// the till that took it, and the caller resumes the new held sale in the
// same request.
func (r *KioskCounterOrdersRepo) ConvertOpenToHeld(ctx context.Context, h HeldSale) (converted bool, err error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin counter order convert: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once Commit succeeds
	res, err := tx.ExecContext(ctx, `
UPDATE kiosk_counter_orders SET status = ? WHERE id = ? AND status = ?`,
		KioskCounterOrderStatusHeld, h.ID, KioskCounterOrderStatusOpen)
	if err != nil {
		return false, fmt.Errorf("mark counter order held: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return false, fmt.Errorf("mark counter order held: rows affected: %w", err)
	} else if n == 0 {
		return false, nil
	}
	// Same columns as HeldSalesRepo.Upsert's insert branch: created_at kept
	// from the caller (the order's own age), updated_at now, not yet
	// confirmed on any primary (a local-only row, resumed straight away).
	if _, err := tx.ExecContext(ctx, `
INSERT INTO held_sales (id, label, total_minor, line_count, payload, table_id, created_at, updated_at, primary_synced)
VALUES (?, ?, ?, ?, ?, ?, COALESCE(NULLIF(?, ''), datetime('now')), datetime('now'), 0)`,
		h.ID, h.Label, h.TotalMinor, h.LineCount, h.Payload, nullIfEmpty(h.TableID), h.CreatedAt); err != nil {
		return false, fmt.Errorf("insert held sale for counter order: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit counter order convert: %w", err)
	}
	return true, nil
}
