package pos

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

type ItemInput struct {
	ID          string
	SKU         string
	Name        string
	BasePrice   int64
	Unit        string
	CategoryID  *string
	BrandID     *string
	TaxCodeID   *string
	IsWeighed   bool
	Description string
	IsActive    bool
}

type VariantInput struct {
	ID        string
	ItemID    string
	SKU       string
	Name      string
	Price     int64
	CostPrice *int64
	IsActive  bool
}

type BarcodeInput struct {
	Barcode     string
	ItemID      string
	VariantID   string
	BarcodeType string
	IsPrimary   bool
	ForVariant  bool
}

// DeactivateItem sets is_active=0 for an item and its variants.
func DeactivateItem(ctx context.Context, db *sql.DB, itemID string) error {
	if itemID == "" {
		return errors.New("itemID required")
	}
	if _, err := db.ExecContext(ctx, `UPDATE items SET is_active = 0 WHERE id = ?`, itemID); err != nil {
		return fmt.Errorf("deactivate item: %w", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE item_variants SET is_active = 0 WHERE item_id = ?`, itemID); err != nil {
		return fmt.Errorf("deactivate item variants: %w", err)
	}
	return nil
}

// DeactivateVariant sets is_active=0 for a variant.
func DeactivateVariant(ctx context.Context, db *sql.DB, variantID string) error {
	if variantID == "" {
		return errors.New("variantID required")
	}
	if _, err := db.ExecContext(ctx, `UPDATE item_variants SET is_active = 0 WHERE id = ?`, variantID); err != nil {
		return fmt.Errorf("deactivate variant: %w", err)
	}
	return nil
}

// CreateItem inserts a new active item.
func CreateItem(ctx context.Context, db *sql.DB, in ItemInput) (string, error) {
	if in.Name == "" {
		return "", errors.New("name required")
	}
	if in.Unit == "" {
		in.Unit = "each"
	}
	if in.ID == "" {
		in.ID = uuid.NewString()
	}
	if in.SKU == "" {
		in.SKU = in.ID
	}
	active := 1
	if !in.IsActive {
		active = 0
	}
	_, err := db.ExecContext(ctx, `
INSERT INTO items (id, sku, name, description, category_id, brand_id, unit, base_price, tax_code_id, is_active, is_weighed)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, in.ID, in.SKU, in.Name, in.Description, nullable(in.CategoryID), nullable(in.BrandID), in.Unit, in.BasePrice, nullable(in.TaxCodeID), active, boolToInt(in.IsWeighed))
	if err != nil {
		return "", fmt.Errorf("insert item: %w", err)
	}
	return in.ID, nil
}

// UpdateItem updates mutable fields for an item.
func UpdateItem(ctx context.Context, db *sql.DB, in ItemInput) error {
	if in.ID == "" {
		return errors.New("id required")
	}
	if in.Name == "" {
		return errors.New("name required")
	}
	if in.Unit == "" {
		in.Unit = "each"
	}
	active := 1
	if !in.IsActive {
		active = 0
	}
	_, err := db.ExecContext(ctx, `
UPDATE items
SET sku = COALESCE(NULLIF(?, ''), sku),
    name = ?,
    description = ?,
    category_id = ?,
    brand_id = ?,
    unit = ?,
    base_price = ?,
    tax_code_id = ?,
    is_active = ?,
    is_weighed = ?
WHERE id = ?
`, nullableString(in.SKU), in.Name, in.Description, nullable(in.CategoryID), nullable(in.BrandID), in.Unit, in.BasePrice, nullable(in.TaxCodeID), active, boolToInt(in.IsWeighed), in.ID)
	if err != nil {
		return fmt.Errorf("update item: %w", err)
	}
	return nil
}

// CreateVariant inserts a new active variant under an item.
func CreateVariant(ctx context.Context, db *sql.DB, in VariantInput) (string, error) {
	if in.ItemID == "" {
		return "", errors.New("item_id required")
	}
	if in.Name == "" {
		return "", errors.New("name required")
	}
	if in.ID == "" {
		in.ID = uuid.NewString()
	}
	active := 1
	if !in.IsActive {
		active = 0
	}
	_, err := db.ExecContext(ctx, `
INSERT INTO item_variants (id, item_id, sku, name, price, cost_price, is_active)
VALUES (?, ?, ?, ?, ?, ?, ?)
`, in.ID, in.ItemID, nullableString(in.SKU), in.Name, in.Price, nullableInt64(in.CostPrice), active)
	if err != nil {
		return "", fmt.Errorf("insert variant: %w", err)
	}
	return in.ID, nil
}

// UpdateVariant updates mutable fields for a variant.
func UpdateVariant(ctx context.Context, db *sql.DB, in VariantInput) error {
	if in.ID == "" {
		return errors.New("id required")
	}
	if in.Name == "" {
		return errors.New("name required")
	}
	active := 1
	if !in.IsActive {
		active = 0
	}
	_, err := db.ExecContext(ctx, `
UPDATE item_variants
SET sku = COALESCE(NULLIF(?, ''), sku),
    name = ?,
    price = ?,
    cost_price = ?,
    is_active = ?
WHERE id = ?
`, nullableString(in.SKU), in.Name, in.Price, nullableInt64(in.CostPrice), active, in.ID)
	if err != nil {
		return fmt.Errorf("update variant: %w", err)
	}
	return nil
}

// AddBarcode enforces XOR between item_id and variant_id and that the target is active.
func AddBarcode(ctx context.Context, db *sql.DB, in BarcodeInput) error {
	if in.Barcode == "" {
		return errors.New("barcode required")
	}
	if (in.ItemID == "" && in.VariantID == "") || (in.ItemID != "" && in.VariantID != "") {
		return errors.New("provide exactly one of item_id or variant_id")
	}
	if in.BarcodeType == "" {
		in.BarcodeType = "EAN13"
	}
	if in.VariantID != "" {
		if err := ensureBarcodeAvailable(ctx, db, in.Barcode, "variant", in.VariantID); err != nil {
			return err
		}
		var active int
		if err := db.QueryRowContext(ctx, `SELECT is_active FROM item_variants WHERE id = ?`, in.VariantID).Scan(&active); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("variant not found: %s", in.VariantID)
			}
			return err
		}
		if active == 0 {
			return fmt.Errorf("variant %s inactive", in.VariantID)
		}
		_, err := db.ExecContext(ctx, `
INSERT INTO variant_barcodes (barcode, variant_id, barcode_type, is_primary)
VALUES (?, ?, ?, ?)
ON CONFLICT(barcode) DO UPDATE SET variant_id=excluded.variant_id, barcode_type=excluded.barcode_type, is_primary=excluded.is_primary
`, in.Barcode, in.VariantID, in.BarcodeType, boolToInt(in.IsPrimary))
		return err
	}
	// item barcode
	if err := ensureBarcodeAvailable(ctx, db, in.Barcode, "item", in.ItemID); err != nil {
		return err
	}
	var active int
	if err := db.QueryRowContext(ctx, `SELECT is_active FROM items WHERE id = ?`, in.ItemID).Scan(&active); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("item not found: %s", in.ItemID)
		}
		return err
	}
	if active == 0 {
		return fmt.Errorf("item %s inactive", in.ItemID)
	}
	_, err := db.ExecContext(ctx, `
INSERT INTO item_barcodes (barcode, item_id, barcode_type, is_primary)
VALUES (?, ?, ?, ?)
ON CONFLICT(barcode) DO UPDATE SET item_id=excluded.item_id, barcode_type=excluded.barcode_type, is_primary=excluded.is_primary
`, in.Barcode, in.ItemID, in.BarcodeType, boolToInt(in.IsPrimary))
	return err
}

func nullable(s *string) any {
	if s == nil || *s == "" {
		return nil
	}
	return *s
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableInt64(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ensureBarcodeAvailable enforces the (item_id XOR variant_id) rule across barcode tables.
// It permits re-inserting the same barcode for the same target, but blocks cross-target moves.
func ensureBarcodeAvailable(ctx context.Context, db *sql.DB, barcode, targetType, targetID string) error {
	var existingItem string
	switch targetType {
	case "item":
		if err := db.QueryRowContext(ctx, `SELECT item_id FROM item_barcodes WHERE barcode = ?`, barcode).Scan(&existingItem); err == nil {
			if existingItem != targetID {
				return fmt.Errorf("barcode already assigned to item %s", existingItem)
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		// Check variant table for cross-assignment
		if err := db.QueryRowContext(ctx, `SELECT variant_id FROM variant_barcodes WHERE barcode = ?`, barcode).Scan(&existingItem); err == nil {
			return fmt.Errorf("barcode already assigned to variant %s", existingItem)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	case "variant":
		if err := db.QueryRowContext(ctx, `SELECT variant_id FROM variant_barcodes WHERE barcode = ?`, barcode).Scan(&existingItem); err == nil {
			if existingItem != targetID {
				return fmt.Errorf("barcode already assigned to variant %s", existingItem)
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err := db.QueryRowContext(ctx, `SELECT item_id FROM item_barcodes WHERE barcode = ?`, barcode).Scan(&existingItem); err == nil {
			return fmt.Errorf("barcode already assigned to item %s", existingItem)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	default:
		return errors.New("invalid targetType for barcode")
	}
	return nil
}
