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
// empty for a clear recorded on this till (always, today).
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
