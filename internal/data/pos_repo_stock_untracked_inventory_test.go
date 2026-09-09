package data

import (
	"context"
	"testing"
)

// TestGetLowStockItems_ExcludesUntrackedItems covers ut-docs#1850: an item
// flagged stock_untracked must never appear on the low-stock/reorder list,
// even when it would otherwise qualify (below its own reorder level).
func TestGetLowStockItems_ExcludesUntrackedItems(t *testing.T) {
	d, repo := openB8InvDB(t)
	ctx := context.Background()

	seedB8Item(t, d, "b8-tracked", "sku-tracked", "Tracked Low", 10, 1)
	seedB8Inventory(t, d, "b8-tracked", "loc_main", 4)
	seedB8Item(t, d, "b8-untracked", "sku-untracked", "Untracked Low", 10, 1)
	seedB8Inventory(t, d, "b8-untracked", "loc_main", 4)
	mustExec(t, d, `UPDATE items SET stock_untracked = 1 WHERE id = 'b8-untracked'`)

	items, err := repo.GetLowStockItems(ctx, "")
	if err != nil {
		t.Fatalf("GetLowStockItems: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items %+v, want 1 (untracked item must be excluded)", len(items), items)
	}
	if items[0].ItemID != "b8-tracked" {
		t.Fatalf("got item %q, want b8-tracked", items[0].ItemID)
	}
}

// TestListStockLevels_ExcludesUntrackedItems covers the same exclusion for
// the Inventory screen's own listing (ListStockLevels), which — unlike
// GetLowStockItems — is not gated by a reorder level at all, so an
// untracked item with a leftover inventory row (from before being switched
// untracked) would otherwise still show up.
func TestListStockLevels_ExcludesUntrackedItems(t *testing.T) {
	d, repo := openB8InvDB(t)
	ctx := context.Background()

	seedB8Item(t, d, "b8-tracked", "sku-tracked", "Tracked Item", 0, 1)
	seedB8Inventory(t, d, "b8-tracked", "loc_main", 4)
	seedB8Item(t, d, "b8-untracked", "sku-untracked", "Untracked Item", 0, 1)
	seedB8Inventory(t, d, "b8-untracked", "loc_main", 4)
	mustExec(t, d, `UPDATE items SET stock_untracked = 1 WHERE id = 'b8-untracked'`)

	items, err := repo.ListStockLevels(ctx)
	if err != nil {
		t.Fatalf("ListStockLevels: %v", err)
	}
	for _, it := range items {
		if it.ItemID == "b8-untracked" {
			t.Fatalf("untracked item's leftover inventory row must not appear in ListStockLevels, got %+v", it)
		}
	}
	found := false
	for _, it := range items {
		if it.ItemID == "b8-tracked" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected tracked item to still appear, got %+v", items)
	}
}
