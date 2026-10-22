package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"
)

// StockCountMovementType is the stock_movements.type of the movement a
// stock count writes. NetStockMovementSince leaves it out, so applying the
// same count again finds nothing to move (ADR-0168 §4, ut-docs#4749).
const StockCountMovementType = "count"

// StockCountInput is one counted quantity for an item (or variant) at a
// location: Target as of CountedAt, in the till's clock.
type StockCountInput struct {
	ItemID, VariantID, LocationID string
	Target                        float64
	CountedAt                     time.Time
	Reason, ActorID               string
}

// StockCountResult reports what ApplyStockCount did. Delta is the movement
// written (0 = nothing to move); Current is the level before it; Since is
// the net of every other movement recorded since CountedAt.
type StockCountResult struct {
	Delta, Current, Since float64
}

// stockQtyScale keeps float noise out of the ledger: quantities are kept
// to three decimals (weighed goods), the same step the weighed UI uses.
const stockQtyScale = 1000

func roundStockQty(q float64) float64 { return math.Round(q*stockQtyScale) / stockQtyScale }

// ApplyStockCount sets an item's stock to the counted Target while keeping
// the movements recorded after CountedAt. The movement it writes is
//
//	Target + net_movement_since(CountedAt) - current
//
// (the count describes the stock at CountedAt; what moved since stays).
// Read and write share one transaction so a sale cannot slip between them.
// Movements of type "count" are not part of the net, which makes a repeat
// of the same count a zero movement. It writes nothing when the delta
// rounds to zero.
func (r *POSRepo) ApplyStockCount(ctx context.Context, in StockCountInput) (StockCountResult, error) {
	var res StockCountResult
	if in.LocationID == "" {
		return res, errors.New("locationID required")
	}
	if (in.ItemID == "") == (in.VariantID == "") {
		return res, errors.New("exactly one of itemID and variantID required")
	}
	if in.Target < 0 || math.IsNaN(in.Target) || math.IsInf(in.Target, 0) {
		return res, errors.New("target must be a non-negative number")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return res, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	err = tx.QueryRowContext(ctx, `
SELECT quantity FROM inventory
WHERE location_id = ? AND ((item_id = ? AND variant_id IS NULL) OR (variant_id = ? AND item_id IS NULL))
`, in.LocationID, nullIfEmpty(in.ItemID), nullIfEmpty(in.VariantID)).Scan(&res.Current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return res, fmt.Errorf("read inventory: %w", err)
	}
	err = tx.QueryRowContext(ctx, `
SELECT COALESCE(SUM(quantity), 0) FROM stock_movements
WHERE location_id = ? AND type <> ? AND created_at >= ?
  AND ((item_id = ? AND variant_id IS NULL) OR (variant_id = ? AND item_id IS NULL))
`, in.LocationID, StockCountMovementType, in.CountedAt.UTC().Format(time.RFC3339),
		nullIfEmpty(in.ItemID), nullIfEmpty(in.VariantID)).Scan(&res.Since)
	if err != nil {
		return res, fmt.Errorf("sum movements since count: %w", err)
	}
	res.Delta = roundStockQty(in.Target + res.Since - res.Current)
	if res.Delta == 0 {
		return res, nil
	}
	if _, err := r.RecordStockMovement(ctx, tx, StockMovementInput{
		ItemID: in.ItemID, VariantID: in.VariantID, LocationID: in.LocationID,
		Type: StockCountMovementType, Quantity: res.Delta, Reason: in.Reason, ActorID: in.ActorID,
	}); err != nil {
		return StockCountResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return StockCountResult{}, fmt.Errorf("commit stock count: %w", err)
	}
	return res, nil
}
