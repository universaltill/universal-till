package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Catalogue record kinds the write-through's optimistic conflict check
// knows (ut-docs#2817). Each names one shop-wide parent table whose rows
// carry updated_at (items since 001, the other four since
// 065_catalog_updated_at.sql) and whose every repository write advances it.
// Children (item/variant barcodes, modifier options, link rows) are saved
// as part of their parent's edit and have no conflict check of their own.
const (
	CatalogKindItem          = "item"
	CatalogKindVariant       = "variant"
	CatalogKindCategory      = "category"
	CatalogKindButton        = "button"
	CatalogKindModifierGroup = "modifier_group"
)

// catalogUpdatedAtQueries is a fixed allow-list: the kind picks a whole
// query, never a table name spliced into SQL.
var catalogUpdatedAtQueries = map[string]string{
	CatalogKindItem:          `SELECT updated_at FROM items WHERE id = ?`,
	CatalogKindVariant:       `SELECT updated_at FROM item_variants WHERE id = ?`,
	CatalogKindCategory:      `SELECT updated_at FROM categories WHERE id = ?`,
	CatalogKindButton:        `SELECT updated_at FROM shortcut_buttons WHERE barcode = ?`,
	CatalogKindModifierGroup: `SELECT updated_at FROM item_modifier_groups WHERE id = ?`,
}

// CatalogUpdatedAt returns the row's current updated_at, ok=false when no
// such row exists. An unknown kind is an error.
func (r *CatalogRepo) CatalogUpdatedAt(ctx context.Context, kind, id string) (string, bool, error) {
	q, known := catalogUpdatedAtQueries[kind]
	if !known {
		return "", false, fmt.Errorf("catalog updated_at: unknown kind %q", kind)
	}
	var v sql.NullString
	err := r.db.QueryRowContext(ctx, q, id).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("catalog updated_at %s %s: %w", kind, id, err)
	}
	return v.String, true, nil
}

// ActiveItemUpdatedAts returns every active item's updated_at by id, for the
// catalogue grid's cards (the item editor sends the stamp it loaded as the
// write-through's base_updated_at). One query, whatever the catalogue size.
func (r *CatalogRepo) ActiveItemUpdatedAts(ctx context.Context) (map[string]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, COALESCE(updated_at, '') FROM items WHERE is_active = 1`)
	if err != nil {
		return nil, fmt.Errorf("item updated_at: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, v string
		if err := rows.Scan(&id, &v); err != nil {
			return nil, fmt.Errorf("item updated_at: %w", err)
		}
		out[id] = v
	}
	return out, rows.Err()
}
