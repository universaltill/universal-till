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
	CategoryID  *string
	BrandID     *string
	TaxCodeID   *string
	IsWeighed   bool
	Unit        string
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
