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

// BarcodeExists reports whether a barcode is already attached to any item
// or variant (import dedupe).
func (r *CatalogRepo) BarcodeExists(ctx context.Context, barcode string) (bool, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `
SELECT (SELECT COUNT(*) FROM item_barcodes WHERE barcode = ?)
     + (SELECT COUNT(*) FROM variant_barcodes WHERE barcode = ?)`,
		barcode, barcode).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("barcode exists: %w", err)
	}
	return n > 0, nil
}

// SKUExists reports whether an item SKU is taken (import dedupe).
func (r *CatalogRepo) SKUExists(ctx context.Context, sku string) (bool, error) {
	var n int
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM items WHERE sku = ?`, sku).Scan(&n); err != nil {
		return false, fmt.Errorf("sku exists: %w", err)
	}
	return n > 0, nil
}

// EnsureCategory returns the id of a category by name, creating it if
// missing (imports carry category names, not ids).
func (r *CatalogRepo) EnsureCategory(ctx context.Context, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", nil
	}
	var id string
	err := r.db.QueryRowContext(ctx,
		`SELECT id FROM categories WHERE name = ? COLLATE NOCASE`, name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != sql.ErrNoRows {
		return "", fmt.Errorf("find category: %w", err)
	}
	id = uuid.NewString()
	if _, err := r.db.ExecContext(ctx,
		`INSERT INTO categories (id, name) VALUES (?, ?)`, id, name); err != nil {
		return "", fmt.Errorf("create category: %w", err)
	}
	return id, nil
}

// ItemLabel is what a printed product label needs.
type ItemLabel struct {
	Name       string
	PriceMinor int64
	Code       string // primary barcode, else SKU
}

// GetItemLabel loads label data for one item: name, price, and the primary
// barcode (falling back to any barcode, then the SKU).
func (r *CatalogRepo) GetItemLabel(ctx context.Context, itemID string) (ItemLabel, bool, error) {
	var l ItemLabel
	var sku string
	err := r.db.QueryRowContext(ctx, `
SELECT name, base_price, COALESCE(sku, '') FROM items WHERE id = ?`, itemID).
		Scan(&l.Name, &l.PriceMinor, &sku)
	if err == sql.ErrNoRows {
		return ItemLabel{}, false, nil
	}
	if err != nil {
		return ItemLabel{}, false, fmt.Errorf("item label: %w", err)
	}
	_ = r.db.QueryRowContext(ctx, `
SELECT barcode FROM item_barcodes WHERE item_id = ?
ORDER BY is_primary DESC LIMIT 1`, itemID).Scan(&l.Code)
	if l.Code == "" {
		l.Code = sku
	}
	return l, true, nil
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

// ExportRow is one catalog line for the CSV export (G22b — the
// anti-lock-in half of import: a shop can always take its data and leave).
// Columns are chosen so our own importer round-trips the file.
type ExportRow struct {
	Name        string
	SKU         string
	Barcode     string // primary barcode preferred, else any
	PriceMinor  int64
	Category    string
	Description string
	IsWeighed   bool
	Stock       float64
	IsActive    bool
}

// ExportRows reads the whole catalog for export, active items first.
func (r *CatalogRepo) ExportRows(ctx context.Context) ([]ExportRow, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT i.name, COALESCE(i.sku, ''),
       COALESCE((SELECT b.barcode FROM item_barcodes b WHERE b.item_id = i.id
                 ORDER BY b.is_primary DESC, b.barcode LIMIT 1), ''),
       i.base_price, COALESCE(c.name, ''), COALESCE(i.description, ''),
       i.is_weighed,
       COALESCE((SELECT SUM(v.quantity) FROM inventory v WHERE v.item_id = i.id), 0),
       i.is_active
FROM items i LEFT JOIN categories c ON c.id = i.category_id
ORDER BY i.is_active DESC, i.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExportRow
	for rows.Next() {
		var e ExportRow
		if err := rows.Scan(&e.Name, &e.SKU, &e.Barcode, &e.PriceMinor, &e.Category,
			&e.Description, &e.IsWeighed, &e.Stock, &e.IsActive); err != nil {
			return nil, err
		}
		out = append(out, e)
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
