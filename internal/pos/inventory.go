package pos

import (
	"context"
	"database/sql"

	"github.com/universaltill/universal-till/internal/data"
)

type StockMovementInput = data.StockMovementInput
type OverrideNegativeInventory = data.OverrideNegativeInventory
type LowStockItem = data.LowStockItem

// RecordStockMovement creates a stock_movements entry and updates inventory aggregate.
func RecordStockMovement(ctx context.Context, sqlDB *sql.DB, in StockMovementInput) (string, error) {
	return data.NewPOSRepo(sqlDB).RecordStockMovement(ctx, nil, in)
}

// RecordNegativeInventoryOverride writes an audit entry noting the override.
func RecordNegativeInventoryOverride(ctx context.Context, sqlDB *sql.DB, override OverrideNegativeInventory) (string, error) {
	return data.NewPOSRepo(sqlDB).RecordNegativeInventoryOverride(ctx, override)
}

// LowStockItemsFor returns the reorder list — every stock-tracked item
// below its reorder level — with the shop-wide "sell items without tracking
// stock" setting applied (ut-docs#27).
func LowStockItemsFor(ctx context.Context, sqlDB *sql.DB, locationID string, shopStockUntracked bool) ([]LowStockItem, error) {
	return data.NewPOSRepo(sqlDB).LowStockItemsFor(ctx, locationID, shopStockUntracked)
}
