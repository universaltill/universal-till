package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// NoSaleEvent is one "No sale" cash-drawer open (ut-docs#2558, migration
// 064): the drawer was kicked without a sale. Recorded only after the kick
// bytes reached the printer. ActorID is the session user; ApproverID is set
// only when a manager PIN elevated past the cash_adjustment gate. TillID mirrors
// sales.till_id: empty on a till's own opens; set on the main till to the
// reporting till's tills.id when a replica journals its opens over LAN sync D3
// (POST /api/sync/no-sales, ut-docs#3562 — ApplyJournaledNoSaleEvent).
type NoSaleEvent struct {
	ID         string // generated when empty
	CreatedAt  string // RFC 3339; required — local_date derives from it
	RegisterID string
	TillID     string
	ActorID    string
	ApproverID string
	Reason     string
}

// InsertNoSaleEvent writes one no_sale_events row and returns its id.
// local_date is date(created_at, 'localtime') — InsertSale's own rule for
// sales.local_date — so the event lands on the same business date as a
// sale completed at the same instant.
func (r *POSRepo) InsertNoSaleEvent(ctx context.Context, tx *sql.Tx, e NoSaleEvent) (string, error) {
	if e.CreatedAt == "" {
		return "", errors.New("insert no-sale event: CreatedAt is required")
	}
	if e.ID == "" {
		e.ID = uuid.NewString()
	}
	_, err := r.exec(tx).ExecContext(ctx, `
INSERT INTO no_sale_events (id, created_at, local_date, register_id, till_id, actor_id, approver_id, reason)
VALUES (?, ?, COALESCE(date(?, 'localtime'), ''), ?, ?, ?, ?, ?)`,
		e.ID, e.CreatedAt, e.CreatedAt, nullIfEmpty(e.RegisterID), nullIfEmpty(e.TillID),
		nullIfEmpty(e.ActorID), nullIfEmpty(e.ApproverID), nullIfEmpty(e.Reason))
	if err != nil {
		return "", fmt.Errorf("insert no-sale event: %w", err)
	}
	return e.ID, nil
}

// NoSaleOpens is one (business date, till)'s no-sale drawer opens: the
// total and the count per actor id ("" for an event with no actor — the
// same key salesAggregateCashiers gives a sale with no cashier_id).
type NoSaleOpens struct {
	Total   int
	ByActor map[string]int
}

// noSaleTillKeyExpr is tillKeyExpr's rule over no_sale_events (alias e):
// till_id, else register_id, else the caller's own identity; its one '?'
// is selfTill.
const noSaleTillKeyExpr = `COALESCE(NULLIF(e.till_id, ''), NULLIF(e.register_id, ''), ?)`

// NoSaleOpensForTill counts the no-sale drawer opens on one (business date,
// till), in total and per actor.
func (r *POSRepo) NoSaleOpensForTill(ctx context.Context, day, tillID, selfTill string) (NoSaleOpens, error) {
	out := NoSaleOpens{ByActor: map[string]int{}}
	rows, err := r.db.QueryContext(ctx, `
SELECT COALESCE(e.actor_id, '') AS actor, COUNT(*)
FROM no_sale_events e
WHERE e.local_date = date(?) AND `+noSaleTillKeyExpr+` = ?
GROUP BY actor ORDER BY actor`, day, selfTill, tillID)
	if err != nil {
		return out, fmt.Errorf("no-sale opens: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var actor string
		var n int
		if err := rows.Scan(&actor, &n); err != nil {
			return out, fmt.Errorf("scan no-sale opens: %w", err)
		}
		out.ByActor[actor] = n
		out.Total += n
	}
	return out, rows.Err()
}

// LocalNoSaleEventsSince returns up to limit of this till's OWN no-sale
// opens (till_id empty — a row journaled in from another till is never
// re-pushed) strictly after the composite cursor (afterCreatedAt, afterID),
// ordered by (created_at, id). The replica's D3 push loop pages through it
// (ut-docs#3562). The cursor is composite, not created_at alone, because two
// opens in the same second must not be lost: a created_at-only cursor would
// skip the second one forever. An empty cursor starts from the beginning.
func (r *POSRepo) LocalNoSaleEventsSince(ctx context.Context, afterCreatedAt, afterID string, limit int) ([]NoSaleEvent, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, created_at, COALESCE(register_id, ''), COALESCE(actor_id, ''), COALESCE(approver_id, ''), COALESCE(reason, '')
FROM no_sale_events
WHERE (till_id IS NULL OR till_id = '')
  AND (created_at > ? OR (created_at = ? AND id > ?))
ORDER BY created_at, id
LIMIT ?`, afterCreatedAt, afterCreatedAt, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("local no-sale events: %w", err)
	}
	defer rows.Close()
	var out []NoSaleEvent
	for rows.Next() {
		var e NoSaleEvent
		if err := rows.Scan(&e.ID, &e.CreatedAt, &e.RegisterID, &e.ActorID, &e.ApproverID, &e.Reason); err != nil {
			return nil, fmt.Errorf("scan local no-sale event: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ApplyJournaledNoSaleEvent stores one no-sale open a replica journaled to
// this (main) till over LAN sync D3 (ut-docs#3562). Idempotent by id: a
// re-sent event is a no-op and reports applied=false. local_date follows
// InsertNoSaleEvent's rule. TillID is the reporting till's tills.id, set by
// the caller from the authenticated bearer — never from the peer's payload.
func (r *POSRepo) ApplyJournaledNoSaleEvent(ctx context.Context, e NoSaleEvent) (bool, error) {
	switch {
	case e.ID == "":
		return false, errors.New("apply journaled no-sale event: ID is required")
	case e.CreatedAt == "":
		return false, errors.New("apply journaled no-sale event: CreatedAt is required")
	case e.TillID == "":
		return false, errors.New("apply journaled no-sale event: TillID is required")
	}
	res, err := r.db.ExecContext(ctx, `
INSERT INTO no_sale_events (id, created_at, local_date, register_id, till_id, actor_id, approver_id, reason)
VALUES (?, ?, COALESCE(date(?, 'localtime'), ''), ?, ?, ?, ?, ?)
ON CONFLICT(id) DO NOTHING`,
		e.ID, e.CreatedAt, e.CreatedAt, nullIfEmpty(e.RegisterID), e.TillID,
		nullIfEmpty(e.ActorID), nullIfEmpty(e.ApproverID), nullIfEmpty(e.Reason))
	if err != nil {
		return false, fmt.Errorf("apply journaled no-sale event: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("apply journaled no-sale event: %w", err)
	}
	return n == 1, nil
}

// NewestLocalJournalEntryAt returns the greatest created_at among the
// no-sale opens and cleared baskets with an empty till_id (the filter
// LocalNoSaleEventsSince / LocalClearedBasketsSince use: this till's own,
// plus a replica's rows inherited from its main till at join), or "" when
// there are none. created_at is app-written RFC 3339 UTC seconds in both tables,
// so the text MAX is the newest instant. The restore check uses it so
// sync.restored_since is never earlier than an entry the original till
// stamped with a clock that ran ahead of the copy's (ut-docs#5185).
func (r *POSRepo) NewestLocalJournalEntryAt(ctx context.Context) (string, error) {
	var newest sql.NullString
	err := r.db.QueryRowContext(ctx, `
SELECT MAX(created_at) FROM (
  SELECT created_at FROM no_sale_events WHERE till_id IS NULL OR till_id = ''
  UNION ALL
  SELECT created_at FROM cleared_baskets WHERE till_id IS NULL OR till_id = ''
)`).Scan(&newest)
	if err != nil {
		return "", fmt.Errorf("newest local journal entry: %w", err)
	}
	return newest.String, nil
}

// NoSaleOperator is one cashier's no-sale drawer-open count in a Z window
// (ut-docs#5753).
type NoSaleOperator struct {
	CashierID   string `json:"cashier_id"`   // "" = no actor recorded
	DisplayName string `json:"display_name"` // "" -> the renderer prints reports.eod.unattributed
	Count       int    `json:"count"`
}

// NoSaleCountForDateRange counts the no-sale drawer opens of every till on
// the local-calendar days from..to inclusive (ut-docs#5753).
func (r *POSRepo) NoSaleCountForDateRange(ctx context.Context, from, to string) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM no_sale_events
WHERE local_date BETWEEN date(?) AND date(?)`, from, to).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("no-sale count for date range: %w", err)
	}
	return n, nil
}

// NoSaleCountForInstantWindow is NoSaleCountForDateRange over the
// close-to-close instant window [from, to) (zero from = unbounded).
func (r *POSRepo) NoSaleCountForInstantWindow(ctx context.Context, from, to time.Time) (int, error) {
	win, args := instantWindow("created_at", from, to)
	var n int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM no_sale_events WHERE `+win, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("no-sale count for instant window: %w", err)
	}
	return n, nil
}

// noSaleOperators groups no-sale opens per actor; the display name falls
// back exactly like OperatorSalesForDay (display_name, username, raw id,
// ""). where is the caller's window predicate.
func (r *POSRepo) noSaleOperators(ctx context.Context, where string, args []any) ([]NoSaleOperator, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT COALESCE(e.actor_id, '') AS cashier_id,
       COALESCE(NULLIF(u.display_name, ''), u.username, e.actor_id, '') AS display_name,
       COUNT(*) AS cnt
FROM no_sale_events e
LEFT JOIN users u ON u.id = e.actor_id
WHERE `+where+`
GROUP BY COALESCE(e.actor_id, '')
ORDER BY cnt DESC, display_name, cashier_id`, args...)
	if err != nil {
		return nil, fmt.Errorf("no-sale operators: %w", err)
	}
	defer rows.Close()
	var out []NoSaleOperator
	for rows.Next() {
		var o NoSaleOperator
		if err := rows.Scan(&o.CashierID, &o.DisplayName, &o.Count); err != nil {
			return nil, fmt.Errorf("no-sale operators scan: %w", err)
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("no-sale operators rows: %w", err)
	}
	return out, nil
}

// NoSaleOperatorsForDay lists the per-cashier no-sale opens of one local day.
func (r *POSRepo) NoSaleOperatorsForDay(ctx context.Context, day string) ([]NoSaleOperator, error) {
	return r.noSaleOperators(ctx, `e.local_date = date(?)`, []any{day})
}

// NoSaleOperatorsForInstantWindow is the close-to-close sibling.
func (r *POSRepo) NoSaleOperatorsForInstantWindow(ctx context.Context, from, to time.Time) ([]NoSaleOperator, error) {
	win, args := instantWindow("e.created_at", from, to)
	return r.noSaleOperators(ctx, win, args)
}

// noSaleShiftWhere attributes a no-sale open to ONE shift (ut-docs#5755)
// with SumCashPaymentsForShift's rule: same register, created_at at or after
// opened_at and, once the shift is closed, at or before closed_at (inclusive,
// as the cash takings are) — an open shift is unbounded above. Both sides go
// through datetime(...) (instantWindow's normalisation, ADR-0066): opened_at
// may be the 001 schema default's 'YYYY-MM-DD HH:MM:SS' form while
// created_at is RFC 3339, and a text compare would misorder them. till_id is
// not filtered (cash takings aren't either). An event with no register_id
// (EnsureRegister failed when it was recorded) matches no shift; it still
// counts on the EOD. Its one '?' is the shift id; an unknown id matches
// nothing.
const noSaleShiftWhere = `EXISTS (
  SELECT 1 FROM shifts sh
  WHERE sh.id = ?
    AND e.register_id = sh.register_id
    AND datetime(e.created_at) >= datetime(sh.opened_at)
    AND (sh.closed_at IS NULL OR sh.closed_at = ''
         OR datetime(e.created_at) <= datetime(sh.closed_at)))`

// NoSaleForShift returns one shift's no-sale drawer opens (ut-docs#5755): the
// total and the per-cashier breakdown (noSaleOperators' grouping, order and
// name fallback, #5753). Informational only — callers never fold it into
// expected cash, variance, skim or float. An unknown shift id, or a shift with
// no opens, returns (0, nil, nil).
func (r *POSRepo) NoSaleForShift(ctx context.Context, shiftID string) (int, []NoSaleOperator, error) {
	ops, err := r.noSaleOperators(ctx, noSaleShiftWhere, []any{shiftID})
	if err != nil {
		return 0, nil, fmt.Errorf("no-sale for shift: %w", err)
	}
	total := 0
	for _, o := range ops {
		total += o.Count
	}
	return total, ops, nil
}
