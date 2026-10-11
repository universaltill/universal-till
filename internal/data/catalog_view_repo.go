package data

import (
	"context"
	"fmt"
	"sort"

	"github.com/universaltill/universal-till/internal/itemimages"
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

// CatalogPhotoViewRow is one row of the catalog.photos.v1 core read view
// (ut-docs#4094): an active item that has a reference photo. Source is
// "ai_ref" (a cashier-confirmed photo) or "thumb" (the catalog thumbnail);
// PhotoAt is that file's modification time in Unix seconds. Its JSON shape
// is a published contract (TestCoreViewRowShapesArePinned).
type CatalogPhotoViewRow struct {
	ItemID  string `json:"item_id"`
	Source  string `json:"source"`
	PhotoAt int64  `json:"photo_at"`
}

// ListCatalogPhotoViewRows returns one page of catalog.photos.v1: every
// active item with a reference photo (itemimages.PhotoStamp — a stat, no
// decode), "ai_ref" rows before "thumb" rows, newest first within each,
// then by id. The whole set is built and sorted per call (two stats per
// active item), so pages share one order but are not a snapshot.
func (r *CatalogRepo) ListCatalogPhotoViewRows(ctx context.Context, offset, limit int) ([]CatalogPhotoViewRow, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id FROM items WHERE is_active = 1`)
	if err != nil {
		return nil, fmt.Errorf("list catalog photo view items: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan catalog photo view item: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	all := make([]CatalogPhotoViewRow, 0, len(ids))
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		src, at, ok := itemimages.PhotoStamp(id)
		if !ok {
			continue
		}
		all = append(all, CatalogPhotoViewRow{ItemID: id, Source: src, PhotoAt: at.Unix()})
	}
	sort.Slice(all, func(i, j int) bool {
		a, b := all[i], all[j]
		if (a.Source == itemimages.RoleAIRef) != (b.Source == itemimages.RoleAIRef) {
			return a.Source == itemimages.RoleAIRef
		}
		if a.PhotoAt != b.PhotoAt {
			return a.PhotoAt > b.PhotoAt
		}
		return a.ItemID < b.ItemID
	})
	if offset >= len(all) {
		return []CatalogPhotoViewRow{}, nil
	}
	end := min(offset+limit, len(all))
	return all[offset:end], nil
}
