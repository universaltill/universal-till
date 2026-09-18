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

// GetLowStockItems returns all items where current inventory is below reorder level.
func GetLowStockItems(ctx context.Context, sqlDB *sql.DB, locationID string) ([]LowStockItem, error) {
	return data.NewPOSRepo(sqlDB).GetLowStockItems(ctx, locationID)
}
