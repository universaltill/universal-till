package data

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/universaltill/universal-till/internal/money"
)

// ClearedBasket is one live cashier basket cleared with New sale before any
// tender and without being re-parked (ut-docs#1122, migration 086).
// LineCount is priced lines plus "add by hand" lines; Total is the basket
// total at the moment of clearing. TillID mirrors no_sale_events.till_id:
// empty for a clear recorded on this till; on the main till, a clear a
// replica journaled in (ApplyJournaledClearedBasket, ut-docs#4711) carries
// the reporting till's tills.id.
type ClearedBasket struct {
	ID         string // generated when empty
	CreatedAt  string // RFC 3339; required — local_date derives from it
	RegisterID string
	TillID     string
	ActorID    string
	LineCount  int
	Total      money.Money
}

// InsertClearedBasket writes one cleared_baskets row and returns its id.
// local_date is date(created_at, 'localtime') — InsertSale's own rule for
// sales.local_date — so the clear lands on the same business date as a sale
// completed at the same instant.
func (r *POSRepo) InsertClearedBasket(ctx context.Context, e ClearedBasket) (string, error) {
	if e.CreatedAt == "" {
		return "", errors.New("insert cleared basket: CreatedAt is required")
	}
	if e.ID == "" {
		e.ID = uuid.NewString()
	}
	_, err := r.db.ExecContext(ctx, `
INSERT INTO cleared_baskets (id, created_at, local_date, register_id, till_id, actor_id, line_count, total_minor)
VALUES (?, ?, COALESCE(date(?, 'localtime'), ''), ?, ?, ?, ?, ?)`,
		e.ID, e.CreatedAt, e.CreatedAt, nullIfEmpty(e.RegisterID), nullIfEmpty(e.TillID),
		nullIfEmpty(e.ActorID), e.LineCount, e.Total.Minor())
	if err != nil {
		return "", fmt.Errorf("insert cleared basket: %w", err)
	}
	return e.ID, nil
}

// LocalClearedBasketsSince returns up to limit of this till's OWN cleared
// baskets (till_id empty — a row journaled in from another till is never
// re-pushed) strictly after the composite cursor (afterCreatedAt, afterID),
// ordered by (created_at, id). The replica's D3 push loop pages through it
// (ut-docs#4711); the cursor is composite for the reason
// LocalNoSaleEventsSince's is. An empty cursor starts from the beginning.
func (r *POSRepo) LocalClearedBasketsSince(ctx context.Context, afterCreatedAt, afterID string, limit int) ([]ClearedBasket, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, created_at, COALESCE(register_id, ''), COALESCE(actor_id, ''), line_count, total_minor
FROM cleared_baskets
WHERE (till_id IS NULL OR till_id = '')
  AND (created_at > ? OR (created_at = ? AND id > ?))
ORDER BY created_at, id
LIMIT ?`, afterCreatedAt, afterCreatedAt, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("local cleared baskets: %w", err)
	}
	defer rows.Close()
	var out []ClearedBasket
	for rows.Next() {
		var e ClearedBasket
		var total int64
		if err := rows.Scan(&e.ID, &e.CreatedAt, &e.RegisterID, &e.ActorID, &e.LineCount, &total); err != nil {
			return nil, fmt.Errorf("scan local cleared basket: %w", err)
		}
		e.Total = money.FromMinor(total)
		out = append(out, e)
	}
	return out, rows.Err()
}

// ApplyJournaledClearedBasket stores one cleared basket a replica journaled
// to this (main) till over LAN sync D3 (ut-docs#4711). Idempotent by id: a
// re-sent row is a no-op and reports applied=false. local_date follows
// InsertClearedBasket's rule. TillID is the reporting till's tills.id, set by
// the caller from the authenticated bearer — never from the peer's payload.
func (r *POSRepo) ApplyJournaledClearedBasket(ctx context.Context, e ClearedBasket) (bool, error) {
	switch {
	case e.ID == "":
		return false, errors.New("apply journaled cleared basket: ID is required")
	case e.CreatedAt == "":
		return false, errors.New("apply journaled cleared basket: CreatedAt is required")
	case e.TillID == "":
		return false, errors.New("apply journaled cleared basket: TillID is required")
	}
	res, err := r.db.ExecContext(ctx, `
INSERT INTO cleared_baskets (id, created_at, local_date, register_id, till_id, actor_id, line_count, total_minor)
VALUES (?, ?, COALESCE(date(?, 'localtime'), ''), ?, ?, ?, ?, ?)
ON CONFLICT(id) DO NOTHING`,
		e.ID, e.CreatedAt, e.CreatedAt, nullIfEmpty(e.RegisterID), e.TillID,
		nullIfEmpty(e.ActorID), e.LineCount, e.Total.Minor())
	if err != nil {
		return false, fmt.Errorf("apply journaled cleared basket: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("apply journaled cleared basket: %w", err)
	}
	return n == 1, nil
}

// clearedBasketTillKeyExpr is tillKeyExpr's rule over cleared_baskets (alias
// c): till_id, else register_id, else the caller's own identity; its one '?'
// is selfTill.
const clearedBasketTillKeyExpr = `COALESCE(NULLIF(c.till_id, ''), NULLIF(c.register_id, ''), ?)`

// ClearedBasketsForTill counts the baskets cleared before payment on one
// (business date, till) and sums their totals (ut-docs#4721).
func (r *POSRepo) ClearedBasketsForTill(ctx context.Context, day, tillID, selfTill string) (count int, total money.Money, err error) {
	var minor int64
	err = r.db.QueryRowContext(ctx, `
SELECT COUNT(*), COALESCE(SUM(c.total_minor), 0)
FROM cleared_baskets c
WHERE c.local_date = date(?) AND `+clearedBasketTillKeyExpr+` = ?`,
		day, selfTill, tillID).Scan(&count, &minor)
	if err != nil {
		return 0, money.FromMinor(0), fmt.Errorf("cleared baskets for till: %w", err)
	}
	return count, money.FromMinor(minor), nil
}
