package pos

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
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

// AppendPriceHistoryItem ends the current open price (if any) and appends a new price_history row for an item.
func AppendPriceHistoryItem(ctx context.Context, db *sql.DB, itemID string, price int64, startsAt time.Time) error {
	if itemID == "" {
		return errors.New("itemID required")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE price_history SET ends_at = ? WHERE item_id = ? AND ends_at IS NULL`, startsAt.Format(time.RFC3339), itemID); err != nil {
		return fmt.Errorf("close previous price: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO price_history(id, item_id, price, starts_at) VALUES(?,?,?,?)`, uuidString(), itemID, price, startsAt.Format(time.RFC3339)); err != nil {
		return fmt.Errorf("insert price_history: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit price update: %w", err)
	}
	return nil
}

// AppendPriceHistoryVariant ends the current open price (if any) and appends a new price_history row for a variant.
func AppendPriceHistoryVariant(ctx context.Context, db *sql.DB, variantID string, price int64, startsAt time.Time) error {
	if variantID == "" {
		return errors.New("variantID required")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE price_history SET ends_at = ? WHERE variant_id = ? AND ends_at IS NULL`, startsAt.Format(time.RFC3339), variantID); err != nil {
		return fmt.Errorf("close previous price: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO price_history(id, variant_id, price, starts_at) VALUES(?,?,?,?)`, uuidString(), variantID, price, startsAt.Format(time.RFC3339)); err != nil {
		return fmt.Errorf("insert price_history: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit price update: %w", err)
	}
	return nil
}

func uuidString() string { return uuid.New().String() }

func lookupPriceHistory(ctx context.Context, db *sql.DB, column, id string) (int64, bool, error) {
	query := fmt.Sprintf(`
SELECT price
FROM price_history
WHERE %s = ?
  AND datetime(starts_at) <= CURRENT_TIMESTAMP
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
