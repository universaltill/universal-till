package data

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/universaltill/universal-till/internal/pos"
)

type CatalogRepo struct {
	db *sql.DB
}

func NewCatalogRepo(db *sql.DB) *CatalogRepo {
	return &CatalogRepo{db: db}
}

func (r *CatalogRepo) ListItems(ctx context.Context) ([]pos.ItemInput, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, sku, name, description, category_id, brand_id, unit, base_price, tax_code_id, is_active, is_weighed FROM items WHERE is_active = 1 ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pos.ItemInput
	for rows.Next() {
		var itm pos.ItemInput
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
