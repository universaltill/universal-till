package pos

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ResolveCurrentPrice returns the active price (minor units) for an item or variant.
// Prefers the latest price_history row with open-ended or future end date; falls back to base price.
func ResolveCurrentPrice(ctx context.Context, db *sql.DB, itemID, variantID string) (int64, error) {
	if (itemID == "" && variantID == "") || (itemID != "" && variantID != "") {
		return 0, errors.New("resolve price requires exactly one of itemID or variantID")
	}

	if variantID != "" {
		if price, ok, err := lookupPriceHistory(ctx, db, "variant_id", variantID); err != nil {
			return 0, err
		} else if ok {
			return price, nil
		}
		var price int64
		if err := db.QueryRowContext(ctx, `SELECT price FROM item_variants WHERE id = ? AND is_active = 1`, variantID).Scan(&price); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return 0, fmt.Errorf("variant not found or inactive: %s", variantID)
			}
			return 0, fmt.Errorf("load variant price: %w", err)
		}
		return price, nil
	}

	if price, ok, err := lookupPriceHistory(ctx, db, "item_id", itemID); err != nil {
		return 0, err
	} else if ok {
		return price, nil
	}
	var price int64
	if err := db.QueryRowContext(ctx, `SELECT base_price FROM items WHERE id = ? AND is_active = 1`, itemID).Scan(&price); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, fmt.Errorf("item not found or inactive: %s", itemID)
		}
		return 0, fmt.Errorf("load item price: %w", err)
	}
	return price, nil
}

func lookupPriceHistory(ctx context.Context, db *sql.DB, column, id string) (int64, bool, error) {
	query := fmt.Sprintf(`
SELECT price
FROM price_history
WHERE %s = ?
  AND (ends_at IS NULL OR ends_at > CURRENT_TIMESTAMP)
ORDER BY datetime(starts_at) DESC
LIMIT 1
`, column)
	var price int64
	if err := db.QueryRowContext(ctx, query, id).Scan(&price); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("lookup price_history: %w", err)
	}
	return price, true, nil
}
