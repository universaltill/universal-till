package pos

import (
	"context"
	"database/sql"
	"errors"

	"github.com/universaltill/universal-till/internal/data"
)

type StockMovementInput = data.StockMovementInput
type OverrideNegativeInventory = data.OverrideNegativeInventory
type LowStockItem = data.LowStockItem

// AggregateInventory retrieves current inventory quantity from inventory table for a given item+location.
func AggregateInventory(ctx context.Context, sqlDB *sql.DB, locationID, itemID, variantID string) (float64, error) {
	return data.NewPOSRepo(sqlDB).AggregateInventory(ctx, nil, locationID, itemID, variantID)
}

// RecordStockMovement creates a stock_movements entry and updates inventory aggregate.
func RecordStockMovement(ctx context.Context, sqlDB *sql.DB, in StockMovementInput) (string, error) {
	return data.NewPOSRepo(sqlDB).RecordStockMovement(ctx, nil, in)
}

// CheckNegativeInventory validates that a proposed sale won't create negative inventory.
func CheckNegativeInventory(ctx context.Context, tx *sql.Tx, locationID, itemID, variantID string, requestedQty float64) error {
	if tx == nil {
		return errors.New("transaction required")
	}
	return data.NewPOSRepo(nil).CheckNegativeInventory(ctx, tx, locationID, itemID, variantID, requestedQty)
}

// RecordNegativeInventoryOverride writes an audit entry noting the override.
func RecordNegativeInventoryOverride(ctx context.Context, sqlDB *sql.DB, override OverrideNegativeInventory) (string, error) {
	return data.NewPOSRepo(sqlDB).RecordNegativeInventoryOverride(ctx, override)
}

// GetLowStockItems returns all items where current inventory is below reorder level.
func GetLowStockItems(ctx context.Context, sqlDB *sql.DB, locationID string) ([]LowStockItem, error) {
	return data.NewPOSRepo(sqlDB).GetLowStockItems(ctx, locationID)
}
