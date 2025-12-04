package pos

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/universaltill/universal-till/internal/db"
)

// StockMovementInput captures data for a stock movement (receipt, adjustment, transfer, waste)
type StockMovementInput struct {
	ItemID     string
	VariantID  string
	LocationID string
	Type       string  // receive|adjust|transfer|waste
	Quantity   float64 // positive for receipt/adjust-up, negative for adjust-down/waste
	CostPrice  int64   // optional, minor units
	Reason     string
	ActorID    string
}

// AggregateInventory retrieves current inventory quantity from inventory table for a given item+location
func AggregateInventory(ctx context.Context, sqlDB *sql.DB, locationID, itemID, variantID string) (float64, error) {
	if locationID == "" {
		return 0, errors.New("locationID required")
	}
	if itemID == "" && variantID == "" {
		return 0, errors.New("itemID or variantID required")
	}
	if itemID != "" && variantID != "" {
		return 0, errors.New("cannot specify both itemID and variantID")
	}

	var qty float64
	err := sqlDB.QueryRowContext(ctx, `
SELECT COALESCE(quantity, 0)
FROM inventory
WHERE location_id = ?
  AND ((item_id = ? AND variant_id IS NULL) OR (variant_id = ? AND item_id IS NULL))
`, locationID, nullIfEmpty(itemID), nullIfEmpty(variantID)).Scan(&qty)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("aggregate inventory: %w", err)
	}
	return qty, nil
}

// RecordStockMovement creates a stock_movements entry and updates inventory aggregate
func RecordStockMovement(ctx context.Context, sqlDB *sql.DB, in StockMovementInput) (string, error) {
	if in.LocationID == "" {
		return "", errors.New("locationID required")
	}
	if in.ItemID == "" && in.VariantID == "" {
		return "", errors.New("itemID or variantID required")
	}
	if in.ItemID != "" && in.VariantID != "" {
		return "", errors.New("cannot specify both itemID and variantID")
	}
	if in.Type == "" {
		return "", errors.New("type required")
	}
	if in.Quantity == 0 {
		return "", errors.New("quantity must be non-zero")
	}

	movementID := uuid.NewString()
	err := db.WithTx(ctx, sqlDB, func(tx *sql.Tx) error {
		now := time.Now().UTC().Format(time.RFC3339)
		if _, err := tx.ExecContext(ctx, `
INSERT INTO stock_movements (id, item_id, variant_id, location_id, sale_line_id, type, quantity, cost_price, created_at)
VALUES (?, ?, ?, ?, NULL, ?, ?, ?, ?)
`, movementID, nullIfEmpty(in.ItemID), nullIfEmpty(in.VariantID), in.LocationID, in.Type, in.Quantity, nullInt64(in.CostPrice), now); err != nil {
			return fmt.Errorf("insert stock movement: %w", err)
		}

		// Update inventory aggregate
		res, err := tx.ExecContext(ctx, `
UPDATE inventory
SET quantity = quantity + ?, updated_at = ?
WHERE location_id = ?
  AND ((item_id = ? AND variant_id IS NULL) OR (variant_id = ? AND item_id IS NULL))
`, in.Quantity, now, in.LocationID, nullIfEmpty(in.ItemID), nullIfEmpty(in.VariantID))
		if err != nil {
			return fmt.Errorf("update inventory: %w", err)
		}
		aff, _ := res.RowsAffected()
		if aff == 0 {
			// Insert new inventory row
			if _, err := tx.ExecContext(ctx, `
INSERT INTO inventory (id, item_id, variant_id, location_id, quantity, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
`, uuid.NewString(), nullIfEmpty(in.ItemID), nullIfEmpty(in.VariantID), in.LocationID, in.Quantity, now); err != nil {
				return fmt.Errorf("insert inventory: %w", err)
			}
		}

		// Audit log
		payload := map[string]any{
			"type":     in.Type,
			"quantity": in.Quantity,
			"reason":   in.Reason,
		}
		payloadJSON, _ := json.Marshal(payload)
		if _, err := tx.ExecContext(ctx, `
INSERT INTO audit_log (id, actor_id, entity_type, entity_id, action, data_json, created_at)
VALUES (?, ?, 'inventory', ?, ?, ?, ?)
`, uuid.NewString(), nullIfEmpty(in.ActorID), movementID, in.Type, string(payloadJSON), now); err != nil {
			return fmt.Errorf("insert audit: %w", err)
		}

		return nil
	})
	if err != nil {
		return "", err
	}
	return movementID, nil
}

// CheckNegativeInventory validates that a proposed sale won't create negative inventory
func CheckNegativeInventory(ctx context.Context, tx *sql.Tx, locationID, itemID, variantID string, requestedQty float64) error {
	if requestedQty <= 0 {
		return nil // no check needed for zero/negative requests
	}

	var currentQty float64
	err := tx.QueryRowContext(ctx, `
SELECT COALESCE(quantity, 0)
FROM inventory
WHERE location_id = ?
  AND ((item_id = ? AND variant_id IS NULL) OR (variant_id = ? AND item_id IS NULL))
`, locationID, nullIfEmpty(itemID), nullIfEmpty(variantID)).Scan(&currentQty)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read inventory: %w", err)
	}

	if currentQty < requestedQty {
		itemRef := valueOrDefault(itemID, variantID)
		return fmt.Errorf("insufficient stock for item %s at location %s (have %.2f, need %.2f)", itemRef, locationID, currentQty, requestedQty)
	}
	return nil
}

// OverrideNegativeInventory records a manager override allowing negative inventory with audit
type OverrideNegativeInventory struct {
	ActorID    string
	Reason     string
	ItemID     string
	VariantID  string
	LocationID string
	QtyBefore  float64
}

func RecordNegativeInventoryOverride(ctx context.Context, sqlDB *sql.DB, override OverrideNegativeInventory) (string, error) {
	if override.ActorID == "" {
		return "", errors.New("actorID required")
	}
	if override.Reason == "" {
		return "", errors.New("reason required")
	}

	overrideID := uuid.NewString()
	snapshot := map[string]any{
		"item_id":     override.ItemID,
		"variant_id":  override.VariantID,
		"location_id": override.LocationID,
		"qty_before":  override.QtyBefore,
	}
	payload := map[string]any{
		"reason":   override.Reason,
		"snapshot": snapshot,
	}
	payloadJSON, _ := json.Marshal(payload)

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := sqlDB.ExecContext(ctx, `
INSERT INTO audit_log (id, actor_id, entity_type, entity_id, action, data_json, created_at)
VALUES (?, ?, 'inventory', ?, 'negative_inventory_override', ?, ?)
`, overrideID, override.ActorID, fmt.Sprintf("%s:%s", override.LocationID, valueOrDefault(override.ItemID, override.VariantID)), string(payloadJSON), now); err != nil {
		return "", fmt.Errorf("insert override audit: %w", err)
	}

	return overrideID, nil
}

// LowStockItem represents an item with low stock
type LowStockItem struct {
	ItemID       string
	Name         string
	SKU          string
	LocationID   string
	LocationName string
	CurrentQty   float64
	ReorderLevel int
}

// GetLowStockItems returns all items where current inventory is below reorder level
func GetLowStockItems(ctx context.Context, sqlDB *sql.DB, locationID string) ([]LowStockItem, error) {
	query := `
SELECT 
	i.id,
	i.name,
	COALESCE(i.sku, ''),
	inv.location_id,
	COALESCE(sl.name, ''),
	COALESCE(inv.quantity, 0),
	i.reorder_level
FROM items i
LEFT JOIN inventory inv ON inv.item_id = i.id
LEFT JOIN stock_locations sl ON sl.id = inv.location_id
WHERE i.reorder_level > 0
  AND COALESCE(inv.quantity, 0) < i.reorder_level
`
	args := []any{}
	if locationID != "" {
		query += ` AND inv.location_id = ?`
		args = append(args, locationID)
	}
	query += ` ORDER BY i.name`

	rows, err := sqlDB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query low stock: %w", err)
	}
	defer rows.Close()

	var items []LowStockItem
	for rows.Next() {
		var item LowStockItem
		if err := rows.Scan(&item.ItemID, &item.Name, &item.SKU, &item.LocationID, &item.LocationName, &item.CurrentQty, &item.ReorderLevel); err != nil {
			return nil, fmt.Errorf("scan low stock item: %w", err)
		}
		items = append(items, item)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate low stock: %w", err)
	}

	return items, nil
}

func nullInt64(n int64) any {
	if n == 0 {
		return nil
	}
	return n
}
