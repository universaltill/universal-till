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
	if err := validateStockCount(in); err != nil {
		return StockCountResult{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return StockCountResult{}, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := r.applyStockCountTx(ctx, tx, in)
	if err != nil {
		return StockCountResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return StockCountResult{}, fmt.Errorf("commit stock count: %w", err)
	}
	return res, nil
}

// ApplyStockCounts applies a whole stock-take in ONE transaction (ut-docs#5852):
// every line gets ApplyStockCount's formula, and any failing line rolls the
// lot back so a half-applied count never exists. Results are index-aligned
// with lines; an error names the failing line ("line 3: ...", zero-based).
func (r *POSRepo) ApplyStockCounts(ctx context.Context, lines []StockCountInput) ([]StockCountResult, error) {
	for i, in := range lines {
		if err := validateStockCount(in); err != nil {
			return nil, fmt.Errorf("line %d: %w", i, err)
		}
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	out := make([]StockCountResult, len(lines))
	for i, in := range lines {
		res, err := r.applyStockCountTx(ctx, tx, in)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", i, err)
		}
		out[i] = res
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit stock count: %w", err)
	}
	return out, nil
}

func validateStockCount(in StockCountInput) error {
	if in.LocationID == "" {
		return errors.New("locationID required")
	}
	if (in.ItemID == "") == (in.VariantID == "") {
		return errors.New("exactly one of itemID and variantID required")
	}
	if in.Target < 0 || math.IsNaN(in.Target) || math.IsInf(in.Target, 0) {
		return errors.New("target must be a non-negative number")
	}
	return nil
}

// applyStockCountTx is the read/sum/write body of a count, on the caller's tx.
func (r *POSRepo) applyStockCountTx(ctx context.Context, tx *sql.Tx, in StockCountInput) (StockCountResult, error) {
	var res StockCountResult
	err := tx.QueryRowContext(ctx, `
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
	return res, nil
}

// StockTakeRow is one line a till stock-take can count at a location
// (ut-docs#5852): an active, stock-tracked item with no active variant, or
// an active variant of one. Quantity is the level at that location (0 when
// never stocked there).
type StockTakeRow struct {
	ItemID, VariantID, Name, VariantName, SKU, CategoryID string
	Quantity                                              float64
	IsWeighed                                             bool
}

// ListStockTakeRows returns every countable line for locationID, ordered by
// name. It is both the count list and the validator for a submitted take.
func (r *POSRepo) ListStockTakeRows(ctx context.Context, locationID string) ([]StockTakeRow, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT i.id, '', i.name, '', COALESCE(i.sku, ''), COALESCE(i.category_id, ''),
       COALESCE(inv.quantity, 0), i.is_weighed
FROM items i
LEFT JOIN inventory inv ON inv.item_id = i.id AND inv.variant_id IS NULL AND inv.location_id = ?
WHERE i.is_active = 1 AND i.stock_untracked = 0
  AND NOT EXISTS (SELECT 1 FROM item_variants v WHERE v.item_id = i.id AND v.is_active = 1)
UNION ALL
SELECT i.id, v.id, i.name, v.name, COALESCE(v.sku, ''), COALESCE(i.category_id, ''),
       COALESCE(inv.quantity, 0), i.is_weighed
FROM item_variants v
JOIN items i ON i.id = v.item_id
LEFT JOIN inventory inv ON inv.variant_id = v.id AND inv.item_id IS NULL AND inv.location_id = ?
WHERE i.is_active = 1 AND v.is_active = 1 AND i.stock_untracked = 0
ORDER BY 3 COLLATE NOCASE, 4 COLLATE NOCASE`, locationID, locationID)
	if err != nil {
		return nil, fmt.Errorf("list stock-take rows: %w", err)
	}
	defer rows.Close()
	var out []StockTakeRow
	for rows.Next() {
		var s StockTakeRow
		var weighed int
		if err := rows.Scan(&s.ItemID, &s.VariantID, &s.Name, &s.VariantName, &s.SKU, &s.CategoryID, &s.Quantity, &weighed); err != nil {
			return nil, fmt.Errorf("scan stock-take row: %w", err)
		}
		s.IsWeighed = weighed != 0
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate stock-take rows: %w", err)
	}
	return out, nil
}
