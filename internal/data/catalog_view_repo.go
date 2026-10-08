package data

import (
	"context"
	"fmt"
)

// CatalogViewItem is one row of the catalog.items.v1 core read view
// (ADR-0149 §6, ut-docs#3698). Its JSON shape is a published contract
// (ut-docs reference/contracts/plugin-views.md, pinned by
// TestCoreViewRowShapesArePinned): never add a page-only field here.
type CatalogViewItem struct {
	ID         string `json:"id"`
	SKU        string `json:"sku"`
	Name       string `json:"name"`
	CategoryID string `json:"category_id"`
	Category   string `json:"category"`
	PriceMinor int64  `json:"price_minor"` // per Unit: per kg/g for a weighed item
	Unit       string `json:"unit"`
	Weighed    bool   `json:"weighed"`
	Active     bool   `json:"active"`
}

// ListCatalogViewItems returns one page of the catalog for
// catalog.items.v1: every item, inactive and sample ones included (Active
// says which), ordered by name (SQLite NOCASE: ASCII case-insensitive) then id so offset paging is stable.
// A missing SKU or category reads as "".
func (r *CatalogRepo) ListCatalogViewItems(ctx context.Context, offset, limit int) ([]CatalogViewItem, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT i.id, COALESCE(i.sku, ''), i.name, COALESCE(i.category_id, ''),
		       COALESCE(c.name, ''), i.base_price, i.unit, i.is_weighed, i.is_active
		FROM items i
		LEFT JOIN categories c ON c.id = i.category_id
		ORDER BY i.name COLLATE NOCASE, i.id
		LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list catalog view items: %w", err)
	}
	defer rows.Close()
	out := []CatalogViewItem{}
	for rows.Next() {
		var it CatalogViewItem
		var weighed, active int
		if err := rows.Scan(&it.ID, &it.SKU, &it.Name, &it.CategoryID, &it.Category, &it.PriceMinor, &it.Unit, &weighed, &active); err != nil {
			return nil, fmt.Errorf("scan catalog view item: %w", err)
		}
		it.Weighed = weighed != 0
		it.Active = active != 0
		out = append(out, it)
	}
	return out, rows.Err()
}
