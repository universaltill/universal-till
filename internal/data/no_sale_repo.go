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
// sales.till_id for a replica's journaled open; nothing journals no-sale
// events to the main till yet (ut-docs#3562), so it is empty today.
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
