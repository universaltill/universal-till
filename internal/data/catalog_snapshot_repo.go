package data

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
)

// SnapshotVariant is one variant nested under its item in the catalog
// snapshot (schema 2, contract §3.7).
type SnapshotVariant struct {
	ID         string
	Name       string
	SKU        string
	PriceMinor int64
	Active     bool
	Barcodes   []string
}

// SnapshotItem is one item of the catalog snapshot (schema 2). Every list
// is non-nil (the wire shape is [] never null).
type SnapshotItem struct {
	ID                        string
	Name                      string
	SKU                       string
	PriceMinor                int64
	CategoryID                string
	Color                     string
	Active                    bool
	IsWeighed                 bool
	StockUntracked            bool
	Barcodes                  []string // primary first
	ModifierGroupIDs          []string // directly attached, link order
	ModifierOptOutIDs         []string
	EffectiveModifierGroupIDs []string // ResolveGroupsForItem's result, sale-screen order
	Variants                  []SnapshotVariant
}

// CatalogSnapshotItems reads the whole catalog for the cloud snapshot
// (schema 2) in a fixed number of queries — never one per item, so a
// 20 000-item shop costs the same handful of reads as a 20-item one.
// Inactive items are included; the result is ordered active first, then
// by name and id, so a size-capped push drops inactive items first.
//
// EffectiveModifierGroupIDs reproduces ModifierRepo.ResolveGroupsForItem
// (ADR-0094) in bulk: the item's own ACTIVE directly-linked groups in link
// order, then its category's ACTIVE linked groups in the category link's
// order, minus opt-outs and minus groups already linked directly.
func (r *CatalogRepo) CatalogSnapshotItems(ctx context.Context) ([]SnapshotItem, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, name, COALESCE(sku, ''), base_price, COALESCE(category_id, ''), COALESCE(color, ''),
       is_active, is_weighed, stock_untracked
FROM items
ORDER BY is_active DESC, name, id`)
	if err != nil {
		return nil, fmt.Errorf("catalog snapshot: items: %w", err)
	}
	var items []SnapshotItem
	for rows.Next() {
		var it SnapshotItem
		if err := rows.Scan(&it.ID, &it.Name, &it.SKU, &it.PriceMinor, &it.CategoryID, &it.Color, &it.Active, &it.IsWeighed, &it.StockUntracked); err != nil {
			rows.Close()
			return nil, fmt.Errorf("catalog snapshot: items: %w", err)
		}
		items = append(items, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("catalog snapshot: items: %w", err)
	}

	pairs := func(q string) (map[string][]string, error) {
		rows, err := r.db.QueryContext(ctx, q)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := map[string][]string{}
		for rows.Next() {
			var k, v string
			if err := rows.Scan(&k, &v); err != nil {
				return nil, err
			}
			out[k] = append(out[k], v)
		}
		return out, rows.Err()
	}
	barcodes, err := pairs(`SELECT item_id, barcode FROM item_barcodes ORDER BY item_id, is_primary DESC, barcode`)
	if err != nil {
		return nil, fmt.Errorf("catalog snapshot: barcodes: %w", err)
	}
	direct, err := pairs(`SELECT l.item_id, l.group_id FROM item_modifier_group_links l JOIN item_modifier_groups g ON g.id = l.group_id ORDER BY l.item_id, l.sort_order, g.name`)
	if err != nil {
		return nil, fmt.Errorf("catalog snapshot: links: %w", err)
	}
	directActive, err := pairs(`SELECT l.item_id, l.group_id FROM item_modifier_group_links l JOIN item_modifier_groups g ON g.id = l.group_id WHERE g.is_active = 1 ORDER BY l.item_id, l.sort_order, g.name`)
	if err != nil {
		return nil, fmt.Errorf("catalog snapshot: active links: %w", err)
	}
	catActive, err := pairs(`SELECT l.category_id, l.group_id FROM category_modifier_group_links l JOIN item_modifier_groups g ON g.id = l.group_id WHERE g.is_active = 1 ORDER BY l.category_id, l.sort_order, g.name`)
	if err != nil {
		return nil, fmt.Errorf("catalog snapshot: category links: %w", err)
	}
	optOuts, err := pairs(`SELECT item_id, group_id FROM item_modifier_group_opt_outs ORDER BY item_id, group_id`)
	if err != nil {
		return nil, fmt.Errorf("catalog snapshot: opt-outs: %w", err)
	}
	variantBarcodes, err := pairs(`SELECT variant_id, barcode FROM variant_barcodes ORDER BY variant_id, is_primary DESC, barcode`)
	if err != nil {
		return nil, fmt.Errorf("catalog snapshot: variant barcodes: %w", err)
	}
	variants, err := r.snapshotVariants(ctx, variantBarcodes)
	if err != nil {
		return nil, err
	}

	for i := range items {
		it := &items[i]
		it.Barcodes = nonNil(barcodes[it.ID])
		it.ModifierGroupIDs = nonNil(direct[it.ID])
		it.ModifierOptOutIDs = nonNil(optOuts[it.ID])
		it.Variants = variants[it.ID]
		if it.Variants == nil {
			it.Variants = []SnapshotVariant{}
		}
		eff := append([]string{}, directActive[it.ID]...)
		if it.CategoryID != "" {
			skip := map[string]bool{}
			for _, g := range eff {
				skip[g] = true
			}
			for _, g := range optOuts[it.ID] {
				skip[g] = true
			}
			for _, g := range catActive[it.CategoryID] {
				if !skip[g] {
					eff = append(eff, g)
				}
			}
		}
		it.EffectiveModifierGroupIDs = eff
	}
	return items, nil
}

func (r *CatalogRepo) snapshotVariants(ctx context.Context, barcodes map[string][]string) (map[string][]SnapshotVariant, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT item_id, id, name, COALESCE(sku, ''), price, is_active FROM item_variants`)
	if err != nil {
		return nil, fmt.Errorf("catalog snapshot: variants: %w", err)
	}
	defer rows.Close()
	out := map[string][]SnapshotVariant{}
	for rows.Next() {
		var itemID string
		var v SnapshotVariant
		var active sql.NullBool
		if err := rows.Scan(&itemID, &v.ID, &v.Name, &v.SKU, &v.PriceMinor, &active); err != nil {
			return nil, fmt.Errorf("catalog snapshot: variants: %w", err)
		}
		v.Active = active.Bool
		v.Barcodes = nonNil(barcodes[v.ID])
		out[itemID] = append(out[itemID], v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("catalog snapshot: variants: %w", err)
	}
	for _, vs := range out {
		sort.SliceStable(vs, func(a, b int) bool {
			if vs[a].Active != vs[b].Active {
				return vs[a].Active
			}
			if vs[a].Name != vs[b].Name {
				return vs[a].Name < vs[b].Name
			}
			return vs[a].ID < vs[b].ID
		})
	}
	return out, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
