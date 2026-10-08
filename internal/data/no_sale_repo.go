package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

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
