package data

import (
	"context"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/catalogtypes"
)

// TestCreateVariant_UntrackedParent_NoInventoryRow covers the review gap on
// ut-docs#1850: CreateItem correctly skips the zero-quantity inventory row
// for a stock_untracked item, but CreateVariant used to create one for every
// variant added to it afterwards — re-creating exactly the rows the flag
// exists to prevent, and leaking them to export/report plugins through
// StockForExport's variant half. The flag lives on the parent item, so the
// variant must inherit it.
func TestCreateVariant_UntrackedParent_NoInventoryRow(t *testing.T) {
	dbo := openBatchDB(t)
	ctx := context.Background()
	cat := NewCatalogRepo(dbo.DB)

	untrackedID, err := cat.CreateItem(ctx, catalogtypes.ItemInput{
		Name: "Untracked Parent", BasePrice: 100, IsActive: true, StockUntracked: true,
	})
	if err != nil {
		t.Fatalf("CreateItem untracked: %v", err)
	}
	untrackedVarID, err := cat.CreateVariant(ctx, catalogtypes.VariantInput{
		ItemID: untrackedID, Name: "Large", Price: 150, IsActive: true,
	})
	if err != nil {
		t.Fatalf("CreateVariant on untracked parent: %v", err)
	}
	var n int
	if err := dbo.DB.QueryRow(`SELECT COUNT(*) FROM inventory WHERE variant_id = ?`, untrackedVarID).Scan(&n); err != nil {
		t.Fatalf("count variant inventory rows: %v", err)
	}
	if n != 0 {
		t.Fatalf("variant of a stock_untracked item must get no inventory row, got %d", n)
	}

	// Control: a variant of a normal, tracked item still gets its row, so
	// this fix can't be mistaken for having disabled variant stock outright.
	trackedID, err := cat.CreateItem(ctx, catalogtypes.ItemInput{
		Name: "Tracked Parent", BasePrice: 100, IsActive: true,
	})
	if err != nil {
		t.Fatalf("CreateItem tracked: %v", err)
	}
	trackedVarID, err := cat.CreateVariant(ctx, catalogtypes.VariantInput{
		ItemID: trackedID, Name: "Large", Price: 150, IsActive: true,
	})
	if err != nil {
		t.Fatalf("CreateVariant on tracked parent: %v", err)
	}
	if err := dbo.DB.QueryRow(`SELECT COUNT(*) FROM inventory WHERE variant_id = ?`, trackedVarID).Scan(&n); err != nil {
		t.Fatalf("count tracked variant inventory rows: %v", err)
	}
	if n != 1 {
		t.Fatalf("variant of a tracked item must still get its inventory row, got %d", n)
	}
}

// TestStockForExport_ExcludesUntrackedParentVariants covers the same review
// gap from the other side: a variant inventory row that already existed
// before its parent was switched untracked (the row is deliberately kept,
// not destroyed) must not travel out in an export/report plugin's stock
// payload — matching the ListStockLevels/GetLowStockItems exclusions that
// keep the same leftover row off every screen.
func TestStockForExport_ExcludesUntrackedParentVariants(t *testing.T) {
	dbo := openBatchDB(t)
	ctx := context.Background()
	cat := NewCatalogRepo(dbo.DB)

	itemID, err := cat.CreateItem(ctx, catalogtypes.ItemInput{Name: "Was Tracked", BasePrice: 100, IsActive: true})
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}
	varID, err := cat.CreateVariant(ctx, catalogtypes.VariantInput{ItemID: itemID, Name: "Large", Price: 150, IsActive: true})
	if err != nil {
		t.Fatalf("CreateVariant: %v", err)
	}
	mustExec(t, dbo, `UPDATE inventory SET quantity = 3 WHERE variant_id = ?`, varID)

	repo := NewPOSRepo(dbo.DB)
	rows, err := repo.StockForExport(ctx)
	if err != nil {
		t.Fatalf("StockForExport (before): %v", err)
	}
	found := false
	for _, r := range rows {
		if r.VariantID == varID {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected the variant's stock row in the export while its parent is tracked, got %+v", rows)
	}

	mustExec(t, dbo, `UPDATE items SET stock_untracked = 1 WHERE id = ?`, itemID)
	rows, err = repo.StockForExport(ctx)
	if err != nil {
		t.Fatalf("StockForExport (after): %v", err)
	}
	for _, r := range rows {
		if r.VariantID == varID || r.ItemID == itemID {
			t.Fatalf("stock_untracked item's rows must not appear in the export payload, got %+v", r)
		}
	}
}

// TestDeadStock_ExcludesUntrackedItems: an item switched untracked keeps its
// existing inventory row, but that leftover quantity must not resurface as
// "capital tied up in dead stock" on a report for an item the shop has
// explicitly said it does not count.
func TestDeadStock_ExcludesUntrackedItems(t *testing.T) {
	dbo := openBatchDB(t)
	seedBatchCatalog(t, dbo)
	ctx := context.Background()
	repo := NewPOSRepo(dbo.DB)

	from := time.Now().Add(-24 * time.Hour)
	to := time.Now().Add(24 * time.Hour)

	// itmA has an inventory row with qty 7 and no sales in the window.
	rows, err := repo.DeadStock(ctx, from, to, 10)
	if err != nil {
		t.Fatalf("DeadStock (before): %v", err)
	}
	found := false
	for _, r := range rows {
		if r.Name == "Item A" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected the tracked item in dead stock before the flag is set, got %+v", rows)
	}

	mustExec(t, dbo, `UPDATE items SET stock_untracked = 1 WHERE id = 'itmA'`)
	rows, err = repo.DeadStock(ctx, from, to, 10)
	if err != nil {
		t.Fatalf("DeadStock (after): %v", err)
	}
	for _, r := range rows {
		if r.Name == "Item A" {
			t.Fatalf("stock_untracked item must not appear in the dead-stock report, got %+v", r)
		}
	}
}

// TestUpdateItem_SwitchBackToTracked_GetsInventoryRow: an item created
// stock_untracked has no inventory row, so switching it back to tracked has
// to create one — otherwise ListStockLevels (which JOINs inventory) leaves
// it off the Inventory screen entirely, not even listed at zero like every
// other tracked item, until someone happens to record a goods-in for it.
func TestUpdateItem_SwitchBackToTracked_GetsInventoryRow(t *testing.T) {
	dbo := openBatchDB(t)
	ctx := context.Background()
	cat := NewCatalogRepo(dbo.DB)

	id, err := cat.CreateItem(ctx, catalogtypes.ItemInput{
		Name: "Untracked At First", BasePrice: 100, IsActive: true, StockUntracked: true,
	})
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}
	var n int
	if err := dbo.DB.QueryRow(`SELECT COUNT(*) FROM inventory WHERE item_id = ?`, id).Scan(&n); err != nil {
		t.Fatalf("count inventory rows: %v", err)
	}
	if n != 0 {
		t.Fatalf("an untracked item must start with no inventory row, got %d", n)
	}

	in, ok, err := cat.GetItem(ctx, id)
	if err != nil || !ok {
		t.Fatalf("GetItem: ok=%v err=%v", ok, err)
	}
	in.StockUntracked = false
	if err := cat.UpdateItem(ctx, in); err != nil {
		t.Fatalf("UpdateItem: %v", err)
	}

	levels, err := NewPOSRepo(dbo.DB).ListStockLevels(ctx)
	if err != nil {
		t.Fatalf("ListStockLevels: %v", err)
	}
	found := false
	for _, l := range levels {
		if l.ItemID == id {
			found = true
			if l.CurrentQty != 0 {
				t.Fatalf("expected the restored row at zero, got %v", l.CurrentQty)
			}
		}
	}
	if !found {
		t.Fatalf("an item switched back to tracked must appear on the Inventory listing, got %+v", levels)
	}

	// Re-saving must not duplicate the row (INSERT OR IGNORE against
	// ux_inventory_item), or the item's stock would double-count.
	if err := cat.UpdateItem(ctx, in); err != nil {
		t.Fatalf("UpdateItem (again): %v", err)
	}
	if err := dbo.DB.QueryRow(`SELECT COUNT(*) FROM inventory WHERE item_id = ?`, id).Scan(&n); err != nil {
		t.Fatalf("count inventory rows after re-save: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected exactly 1 inventory row after re-saving, got %d", n)
	}
}
