package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// SellScreenRepo is the data half of the cashier sell screen's in-memory
// tile cache (ut-docs#2501, internal/ui's SellScreenCache): the cheap change
// marker every request reads, and the next time-scheduled price change the
// cache must expire at.
type SellScreenRepo struct{ db *sql.DB }

func NewSellScreenRepo(db *sql.DB) *SellScreenRepo { return &SellScreenRepo{db: db} }

// SellScreenVersion reads both change counters the sell screen's tiles
// depend on, in ONE single-row query: sync_admin_version.generation
// (migration 023 — every admin table) and sell_screen_version.generation
// (migration 042 — price_history and item_images). ok is false when either
// row is missing (a hand-edited database — migrations always seed both):
// the caller must then never cache, or it would key on the same "no row"
// reading forever. A missing row is not an error; a failed query is.
func (r *SellScreenRepo) SellScreenVersion(ctx context.Context) (admin, sell int64, ok bool, err error) {
	err = r.db.QueryRowContext(ctx, `
SELECT a.generation, s.generation
FROM sync_admin_version a, sell_screen_version s
WHERE a.id = 1 AND s.id = 1`).Scan(&admin, &sell)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, false, nil
	}
	if err != nil {
		return 0, 0, false, fmt.Errorf("sell screen version: %w", err)
	}
	return admin, sell, true, nil
}

// sqliteDatetimeLayout is SQLite's datetime()/CURRENT_TIMESTAMP text form
// (UTC) — the form ItemCurrentPrices compares price_history.starts_at/
// ends_at in.
const sqliteDatetimeLayout = "2006-01-02 15:04:05"

// NextPriceBoundary returns the earliest price_history starts_at or ends_at
// strictly after now (CURRENT_TIMESTAMP), compared through datetime() exactly
// as ItemCurrentPrices does — the next moment a tile's current price can
// change with no write at all (a scheduled price starting, an override
// ending). Zero time when there is none.
func (r *SellScreenRepo) NextPriceBoundary(ctx context.Context) (time.Time, error) {
	var next sql.NullString
	err := r.db.QueryRowContext(ctx, `
SELECT MIN(b) FROM (
  SELECT datetime(starts_at) AS b FROM price_history
   WHERE datetime(starts_at) > CURRENT_TIMESTAMP
  UNION ALL
  SELECT datetime(ends_at) AS b FROM price_history
   WHERE ends_at IS NOT NULL AND datetime(ends_at) > CURRENT_TIMESTAMP
)`).Scan(&next)
	if err != nil {
		return time.Time{}, fmt.Errorf("next price boundary: %w", err)
	}
	if !next.Valid || next.String == "" {
		return time.Time{}, nil
	}
	t, err := time.ParseInLocation(sqliteDatetimeLayout, next.String, time.UTC)
	if err != nil {
		return time.Time{}, fmt.Errorf("next price boundary: parse %q: %w", next.String, err)
	}
	return t, nil
}
