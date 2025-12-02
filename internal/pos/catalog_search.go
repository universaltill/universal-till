package pos

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// CatalogSearcher provides catalog lookup filtered to active items/variants.
type CatalogSearcher struct {
	db *sql.DB
}

func NewCatalogSearcher(db *sql.DB) *CatalogSearcher {
	return &CatalogSearcher{db: db}
}

// SearchActiveItems returns active items matching the query (name, sku, barcode) with optional limit/offset.
func (c *CatalogSearcher) SearchActiveItems(ctx context.Context, q string, offset, limit int) ([]ItemInput, error) {
	like := "%" + strings.TrimSpace(q) + "%"
	if limit <= 0 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := c.db.QueryContext(ctx, `
SELECT i.id, i.sku, i.name, i.description, i.category_id, i.brand_id, i.unit, i.base_price, i.tax_code_id, i.is_active, i.is_weighed
FROM items i
WHERE i.is_active = 1 AND (
      i.name LIKE ?
   OR i.sku LIKE ?
   OR EXISTS (
        SELECT 1 FROM item_barcodes ib WHERE ib.item_id = i.id AND ib.barcode LIKE ?
   )
)
ORDER BY i.name
LIMIT ? OFFSET ?
`, like, like, like, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("search active items: %w", err)
	}
	defer rows.Close()
	var out []ItemInput
	for rows.Next() {
		var itm ItemInput
		var desc, cat, brand, tax sql.NullString
		if err := rows.Scan(&itm.ID, &itm.SKU, &itm.Name, &desc, &cat, &brand, &itm.Unit, &itm.BasePrice, &tax, &itm.IsActive, &itm.IsWeighed); err != nil {
			return nil, err
		}
		if desc.Valid {
			itm.Description = desc.String
		}
		if cat.Valid {
			itm.CategoryID = &cat.String
		}
		if brand.Valid {
			itm.BrandID = &brand.String
		}
		if tax.Valid {
			itm.TaxCodeID = &tax.String
		}
		out = append(out, itm)
	}
	return out, rows.Err()
}

// LookupActiveVariant returns a variant if active; otherwise error.
func (c *CatalogSearcher) LookupActiveVariant(ctx context.Context, variantID string) (*VariantInput, error) {
	if strings.TrimSpace(variantID) == "" {
		return nil, errors.New("variantID required")
	}
	var v VariantInput
	var cost sql.NullInt64
	if err := c.db.QueryRowContext(ctx, `SELECT id, item_id, sku, name, price, cost_price, is_active FROM item_variants WHERE id = ?`, variantID).
		Scan(&v.ID, &v.ItemID, &v.SKU, &v.Name, &v.Price, &cost, &v.IsActive); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("variant not found: %s", variantID)
		}
		return nil, err
	}
	if cost.Valid {
		v.CostPrice = &cost.Int64
	}
	if !v.IsActive {
		return nil, fmt.Errorf("variant inactive: %s", variantID)
	}
	return &v, nil
}
