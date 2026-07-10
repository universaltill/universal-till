package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/universaltill/universal-till/internal/catalogtypes"
)

type CatalogRepo struct {
	db *sql.DB
}

func NewCatalogRepo(db *sql.DB) *CatalogRepo {
	return &CatalogRepo{db: db}
}

// ItemExists reports whether an item row exists (any active state).
func (r *CatalogRepo) ItemExists(ctx context.Context, itemID string) (bool, error) {
	var one int
	err := r.db.QueryRowContext(ctx, `SELECT 1 FROM items WHERE id = ?`, itemID).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (r *CatalogRepo) ListItems(ctx context.Context) ([]catalogtypes.ItemInput, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, sku, name, description, category_id, brand_id, unit, base_price, tax_code_id, is_active, is_weighed FROM items WHERE is_active = 1 ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []catalogtypes.ItemInput
	for rows.Next() {
		var itm catalogtypes.ItemInput
		var tax, cat, brand, desc sql.NullString
		if err := rows.Scan(&itm.ID, &itm.SKU, &itm.Name, &desc, &cat, &brand, &itm.Unit, &itm.BasePrice, &tax, &itm.IsActive, &itm.IsWeighed); err != nil {
			return nil, err
		}
		if desc.Valid {
			itm.Description = desc.String
		}
		if tax.Valid {
			itm.TaxCodeID = &tax.String
		}
		if cat.Valid {
			itm.CategoryID = &cat.String
		}
		if brand.Valid {
			itm.BrandID = &brand.String
		}
		out = append(out, itm)
	}
	return out, rows.Err()
}

type Lookup struct {
	ID   string
	Name string
}

func (r *CatalogRepo) ReadLookup(ctx context.Context, table string) ([]Lookup, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, name FROM `+table+` WHERE (is_active IS NULL OR is_active = 1) ORDER BY name`)
	if err != nil {
		if strings.Contains(err.Error(), "no such column: is_active") {
			rows, err = r.db.QueryContext(ctx, `SELECT id, name FROM `+table+` ORDER BY name`)
		}
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []Lookup
	for rows.Next() {
		var l Lookup
		if err := rows.Scan(&l.ID, &l.Name); err != nil {
			return nil, err
		}
		res = append(res, l)
	}
	return res, rows.Err()
}

// ValidateLookup checks existence of ids in a lookup table.
func (r *CatalogRepo) ValidateLookup(ctx context.Context, table string, id string, required bool) error {
	if id == "" && !required {
		return nil
	}
	var exists int
	if err := r.db.QueryRowContext(ctx, `SELECT 1 FROM `+table+` WHERE id = ?`, id).Scan(&exists); err != nil {
		return errors.New("invalid " + table + " id")
	}
	return nil
}

func (r *CatalogRepo) DeactivateItem(ctx context.Context, itemID string) error {
	if itemID == "" {
		return errors.New("itemID required")
	}
	if _, err := r.db.ExecContext(ctx, `UPDATE items SET is_active = 0 WHERE id = ?`, itemID); err != nil {
		return fmt.Errorf("deactivate item: %w", err)
	}
	if _, err := r.db.ExecContext(ctx, `UPDATE item_variants SET is_active = 0 WHERE item_id = ?`, itemID); err != nil {
		return fmt.Errorf("deactivate item variants: %w", err)
	}
	return nil
}

func (r *CatalogRepo) DeactivateVariant(ctx context.Context, variantID string) error {
	if variantID == "" {
		return errors.New("variantID required")
	}
	if _, err := r.db.ExecContext(ctx, `UPDATE item_variants SET is_active = 0 WHERE id = ?`, variantID); err != nil {
		return fmt.Errorf("deactivate variant: %w", err)
	}
	return nil
}

func (r *CatalogRepo) CreateItem(ctx context.Context, in catalogtypes.ItemInput) (string, error) {
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
	_, err := r.db.ExecContext(ctx, `
INSERT INTO items (id, sku, name, description, category_id, brand_id, unit, base_price, tax_code_id, is_active, is_weighed)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, in.ID, in.SKU, in.Name, in.Description, nullable(in.CategoryID), nullable(in.BrandID), in.Unit, in.BasePrice, nullable(in.TaxCodeID), active, boolToInt(in.IsWeighed))
	if err != nil {
		return "", fmt.Errorf("insert item: %w", err)
	}
	return in.ID, nil
}

func (r *CatalogRepo) UpdateItem(ctx context.Context, in catalogtypes.ItemInput) error {
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
	_, err := r.db.ExecContext(ctx, `
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

func (r *CatalogRepo) CreateVariant(ctx context.Context, in catalogtypes.VariantInput) (string, error) {
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
	_, err := r.db.ExecContext(ctx, `
INSERT INTO item_variants (id, item_id, sku, name, price, cost_price, is_active)
VALUES (?, ?, ?, ?, ?, ?, ?)
`, in.ID, in.ItemID, nullableString(in.SKU), in.Name, in.Price, nullableInt64(in.CostPrice), active)
	if err != nil {
		return "", fmt.Errorf("insert variant: %w", err)
	}
	return in.ID, nil
}

func (r *CatalogRepo) UpdateVariant(ctx context.Context, in catalogtypes.VariantInput) error {
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
	_, err := r.db.ExecContext(ctx, `
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

func (r *CatalogRepo) AddBarcode(ctx context.Context, in catalogtypes.BarcodeInput) error {
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
		if err := ensureBarcodeAvailable(ctx, r.db, in.Barcode, "variant", in.VariantID); err != nil {
			return err
		}
		var active int
		if err := r.db.QueryRowContext(ctx, `SELECT is_active FROM item_variants WHERE id = ?`, in.VariantID).Scan(&active); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("variant not found: %s", in.VariantID)
			}
			return err
		}
		if active == 0 {
			return fmt.Errorf("variant %s inactive", in.VariantID)
		}
		_, err := r.db.ExecContext(ctx, `
INSERT INTO variant_barcodes (barcode, variant_id, barcode_type, is_primary)
VALUES (?, ?, ?, ?)
ON CONFLICT(barcode) DO UPDATE SET variant_id=excluded.variant_id, barcode_type=excluded.barcode_type, is_primary=excluded.is_primary
`, in.Barcode, in.VariantID, in.BarcodeType, boolToInt(in.IsPrimary))
		return err
	}
	// item barcode
	if err := ensureBarcodeAvailable(ctx, r.db, in.Barcode, "item", in.ItemID); err != nil {
		return err
	}
	var active int
	if err := r.db.QueryRowContext(ctx, `SELECT is_active FROM items WHERE id = ?`, in.ItemID).Scan(&active); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("item not found: %s", in.ItemID)
		}
		return err
	}
	if active == 0 {
		return fmt.Errorf("item %s inactive", in.ItemID)
	}
	_, err := r.db.ExecContext(ctx, `
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
	var existing string
	switch targetType {
	case "item":
		if err := db.QueryRowContext(ctx, `SELECT item_id FROM item_barcodes WHERE barcode = ?`, barcode).Scan(&existing); err == nil {
			if existing != targetID {
				return fmt.Errorf("barcode already assigned to item %s", existing)
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err := db.QueryRowContext(ctx, `SELECT variant_id FROM variant_barcodes WHERE barcode = ?`, barcode).Scan(&existing); err == nil {
			return fmt.Errorf("barcode already assigned to variant %s", existing)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	case "variant":
		if err := db.QueryRowContext(ctx, `SELECT variant_id FROM variant_barcodes WHERE barcode = ?`, barcode).Scan(&existing); err == nil {
			if existing != targetID {
				return fmt.Errorf("barcode already assigned to variant %s", existing)
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err := db.QueryRowContext(ctx, `SELECT item_id FROM item_barcodes WHERE barcode = ?`, barcode).Scan(&existing); err == nil {
			return fmt.Errorf("barcode already assigned to item %s", existing)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	default:
		return errors.New("invalid targetType for barcode")
	}
	return nil
}
