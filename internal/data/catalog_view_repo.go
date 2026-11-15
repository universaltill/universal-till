package data

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

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

// The gaps catalog.gaps.v1 and the catalog page's ?missing= filter look
// for (ut-docs#4128). A mask selects any of them: an item matching ANY
// selected gap is listed.
const (
	GapBarcode  = 1 // no whole-item barcode
	GapImage    = 2 // no photo of its own (a placeholder icon is not one)
	GapPrice    = 4 // base price 0
	GapCategory = 8 // no category
	GapAll      = GapBarcode | GapImage | GapPrice | GapCategory
)

// CatalogGapRow is one row of the catalog.gaps.v1 core read view
// (ut-docs#4128): an active item with at least one selected gap, and which
// of its basics it has. Its JSON shape is a published contract
// (TestCoreViewRowShapesArePinned).
type CatalogGapRow struct {
	ItemID     string `json:"item_id"`
	SKU        string `json:"sku"`
	Name       string `json:"name"`
	Category   string `json:"category"`
	PriceMinor int64  `json:"price_minor"`
	HasBarcode bool   `json:"has_barcode"`
	HasImage   bool   `json:"has_image"`
}

// ownPhotoPrefix is where an operator-uploaded item photo lives
// (SetItemThumbnail callers); an imported placeholder icon is elsewhere.
const ownPhotoPrefix = "/public/assets/items/"

func gapWhere(mask int) string {
	var conds []string
	if mask&GapBarcode != 0 {
		conds = append(conds, `NOT EXISTS (SELECT 1 FROM item_barcodes b WHERE b.item_id = i.id)`)
	}
	if mask&GapImage != 0 {
		conds = append(conds, `NOT EXISTS (SELECT 1 FROM item_images m WHERE m.item_id = i.id AND m.role = 'thumbnail' AND m.path LIKE '`+ownPhotoPrefix+`%')`)
	}
	if mask&GapPrice != 0 {
		conds = append(conds, `i.base_price = 0`)
	}
	if mask&GapCategory != 0 {
		conds = append(conds, `COALESCE(i.category_id, '') = ''`)
	}
	return `i.is_active = 1 AND (` + strings.Join(conds, ` OR `) + `)`
}

// ListCatalogGapRows returns one page of the active items with a selected
// gap, ordered by name (NOCASE) then id. mask must be within 1..GapAll.
func (r *CatalogRepo) ListCatalogGapRows(ctx context.Context, mask, offset, limit int) ([]CatalogGapRow, error) {
	if mask < 1 || mask > GapAll {
		return nil, fmt.Errorf("list catalog gaps: bad mask %d", mask)
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT i.id, COALESCE(i.sku, ''), i.name, COALESCE(c.name, ''), i.base_price,
		       EXISTS (SELECT 1 FROM item_barcodes b WHERE b.item_id = i.id),
		       EXISTS (SELECT 1 FROM item_images m WHERE m.item_id = i.id AND m.role = 'thumbnail' AND m.path LIKE '`+ownPhotoPrefix+`%')
		FROM items i LEFT JOIN categories c ON c.id = i.category_id
		WHERE `+gapWhere(mask)+`
		ORDER BY i.name COLLATE NOCASE, i.id
		LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list catalog gaps: %w", err)
	}
	defer rows.Close()
	out := []CatalogGapRow{}
	for rows.Next() {
		var g CatalogGapRow
		if err := rows.Scan(&g.ItemID, &g.SKU, &g.Name, &g.Category, &g.PriceMinor, &g.HasBarcode, &g.HasImage); err != nil {
			return nil, fmt.Errorf("scan catalog gap: %w", err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// CatalogGapIDs is the set of active item ids with a selected gap (the
// catalog page's ?missing= filter).
func (r *CatalogRepo) CatalogGapIDs(ctx context.Context, mask int) (map[string]bool, error) {
	if mask < 1 || mask > GapAll {
		return nil, fmt.Errorf("catalog gap ids: bad mask %d", mask)
	}
	rows, err := r.db.QueryContext(ctx, `SELECT i.id FROM items i WHERE `+gapWhere(mask))
	if err != nil {
		return nil, fmt.Errorf("catalog gap ids: %w", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// ItemBarcodeCountTx is how many whole-item barcodes an item has, read
// through the caller's transaction (the AI update action's stale guard,
// ut-docs#4128).
func (r *CatalogRepo) ItemBarcodeCountTx(ctx context.Context, tx *sql.Tx, itemID string) (int, error) {
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM item_barcodes WHERE item_id = ?`, itemID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count item barcodes: %w", err)
	}
	return n, nil
}
