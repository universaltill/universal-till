package data

import (
	"context"
	"testing"
)

// TestGetLowStockItems_VariantTrackedItem covers ut-docs#2082: a
// variant-tracked item's inventory rows carry item_id NULL / variant_id set
// (the 001_init.sql CHECK constraint), so the item-scoped query alone never
// sees them — the item read as permanently out of stock (qty always 0)
// regardless of what its variants actually held, so once a reorder_level
// was set it showed as low/needing reorder forever with no way to clear it.
// Fixed the same shape ADR-0043 already established for stock export: a
// separate, additive query per variant row, never folded into the parent
// item's own row (no double-counting).
func TestGetLowStockItems_VariantTrackedItem(t *testing.T) {
	d, repo := openB8InvDB(t)
	ctx := context.Background()

	// Parent item with a reorder level, no item-scoped inventory row of its
	// own — all its stock lives on its variants.
	seedB8Item(t, d, "b8-vlow", "sku-vlow", "B8 Variant Low", 10, 1)
	mustExec(t, d, `INSERT INTO item_variants (id, item_id, sku, name, price) VALUES ('b8-vlow-v1', 'b8-vlow', 'sku-vlow-v1', 'Small', 150)`)
	mustExec(t, d, `INSERT INTO item_variants (id, item_id, sku, name, price) VALUES ('b8-vlow-v2', 'b8-vlow', 'sku-vlow-v2', 'Large', 200)`)
	// Below the reorder level of 10, at loc_main.
	mustExec(t, d, `INSERT INTO inventory (id, item_id, variant_id, location_id, quantity) VALUES ('inv-b8-vlow-v1-main', NULL, 'b8-vlow-v1', 'loc_main', 3)`)
	// At/above the reorder level, at loc_back — must NOT be reported low.
	mustExec(t, d, `INSERT INTO inventory (id, item_id, variant_id, location_id, quantity) VALUES ('inv-b8-vlow-v2-back', NULL, 'b8-vlow-v2', 'loc_back', 20)`)

	items, err := repo.GetLowStockItems(ctx, "")
	if err != nil {
		t.Fatalf("GetLowStockItems: %v", err)
	}
	var v1Row *LowStockItem
	for i, it := range items {
		if it.ItemID != "b8-vlow" {
			continue
		}
		if it.VariantID == "b8-vlow-v2" {
			t.Fatalf("v2 holds 20 >= reorder level 10 at loc_back, must not be reported low: %+v", it)
		}
		if it.VariantID == "b8-vlow-v1" {
			v1Row = &items[i]
		}
	}
	if v1Row == nil {
		t.Fatalf("b8-vlow-v1 must be reported low (qty 3 < reorder level 10), got %+v", items)
	}
	if v1Row.CurrentQty != 3 || v1Row.LocationID != "loc_main" || v1Row.VariantName != "Small" ||
		v1Row.SKU != "sku-vlow-v1" || v1Row.Name != "B8 Variant Low" || v1Row.ReorderLevel != 10 {
		t.Fatalf("b8-vlow-v1 row: %+v, want the variant's own sku/name, qty 3 at loc_main", v1Row)
	}

	// Location filter narrows to that location's variant rows too.
	backOnly, err := repo.GetLowStockItems(ctx, "loc_back")
	if err != nil {
		t.Fatalf("GetLowStockItems(loc_back): %v", err)
	}
	for _, it := range backOnly {
		if it.ItemID == "b8-vlow" {
			t.Fatalf("b8-vlow must not appear for loc_back (v2's 20 >= reorder level 10): %+v", it)
		}
	}

	// A variant with NO inventory row anywhere still belongs on every
	// location's reorder list — same "never stocked" inclusion the
	// item-scoped branch already gives a brand-new item.
	mustExec(t, d, `INSERT INTO item_variants (id, item_id, sku, name, price) VALUES ('b8-vlow-v3', 'b8-vlow', 'sku-vlow-v3', 'Medium', 175)`)
	items, err = repo.GetLowStockItems(ctx, "loc_main")
	if err != nil {
		t.Fatalf("GetLowStockItems(loc_main) after adding unstocked variant: %v", err)
	}
	found := false
	for _, it := range items {
		if it.VariantID == "b8-vlow-v3" {
			found = true
			if it.CurrentQty != 0 {
				t.Fatalf("never-stocked variant must read qty 0, got %+v", it)
			}
		}
	}
	if !found {
		t.Fatalf("never-stocked variant b8-vlow-v3 must appear on loc_main's reorder list, got %+v", items)
	}
}

// TestListStockLevels_VariantTrackedItem_ExcludesUntracked covers the same
// ut-docs#2082 fix for ListStockLevels' variant-scoped rows, plus that they
// respect the existing stock_untracked/is_active exclusions (ut-docs#1850)
// exactly like the item-scoped rows already do — a variant of an untracked
// or inactive parent item must not leak a leftover inventory row onto the
// /inventory screen.
func TestListStockLevels_VariantTrackedItem_ExcludesUntracked(t *testing.T) {
	d, repo := openB8InvDB(t)
	ctx := context.Background()

	seedB8Item(t, d, "b8-vunt", "sku-vunt", "B8 Variant Untracked", 0, 1)
	mustExec(t, d, `INSERT INTO item_variants (id, item_id, sku, name, price) VALUES ('b8-vunt-v1', 'b8-vunt', 'sku-vunt-v1', 'OnlySize', 150)`)
	mustExec(t, d, `INSERT INTO inventory (id, item_id, variant_id, location_id, quantity) VALUES ('inv-b8-vunt-v1', NULL, 'b8-vunt-v1', 'loc_main', 5)`)
	mustExec(t, d, `UPDATE items SET stock_untracked = 1 WHERE id = 'b8-vunt'`)

	levels, err := repo.ListStockLevels(ctx)
	if err != nil {
		t.Fatalf("ListStockLevels: %v", err)
	}
	for _, l := range levels {
		if l.VariantID == "b8-vunt-v1" {
			t.Fatalf("variant of a stock_untracked parent must not appear: %+v", l)
		}
	}
}
