package data

import (
	"context"
	"testing"
)

// findLow returns the row for itemID/variantID in items, or nil.
func findLow(items []LowStockItem, itemID, variantID string) *LowStockItem {
	for i := range items {
		if items[i].ItemID == itemID && items[i].VariantID == variantID {
			return &items[i]
		}
	}
	return nil
}

// TestGetLowStockItems_StockedOnlyElsewhere covers ut-docs#27 (product
// owner's decision, 2026-09-28): a tracked item with a reorder level whose
// only inventory rows are at OTHER locations is listed on this location's
// reorder list at qty 0, marked "not stocked here" — unless the item or the
// shop sells without tracking stock.
func TestGetLowStockItems_StockedOnlyElsewhere(t *testing.T) {
	d, repo := openB8InvDB(t)
	ctx := context.Background()

	// Stocked (plenty) only at loc_back — nothing at loc_main.
	seedB8Item(t, d, "e27-else", "sku-else", "E27A Elsewhere", 5, 1)
	seedB8Inventory(t, d, "e27-else", "loc_back", 40)
	// Stocked at loc_main below its reorder level — listed as today.
	seedB8Item(t, d, "e27-low", "sku-low", "E27B LowHere", 10, 1)
	seedB8Inventory(t, d, "e27-low", "loc_main", 3)
	seedB8Inventory(t, d, "e27-low", "loc_back", 50)
	// Only elsewhere, but sold without tracking stock — never listed.
	seedB8Item(t, d, "e27-untr", "sku-untr", "E27C Untracked", 5, 1)
	seedB8Inventory(t, d, "e27-untr", "loc_back", 1)
	mustExec(t, d, `UPDATE items SET stock_untracked = 1 WHERE id = 'e27-untr'`)
	// Variant stocked only elsewhere — same rule on the variant side.
	seedB8Item(t, d, "e27-var", "sku-var", "E27D Variant", 5, 1)
	mustExec(t, d, `INSERT INTO item_variants (id, item_id, sku, name, price) VALUES ('e27-var-s', 'e27-var', 'sku-var-s', 'Small', 150)`)
	mustExec(t, d, `INSERT INTO inventory (id, item_id, variant_id, location_id, quantity) VALUES ('inv-e27-var-s', NULL, 'e27-var-s', 'loc_back', 30)`)

	items, err := repo.GetLowStockItems(ctx, "loc_main")
	if err != nil {
		t.Fatalf("GetLowStockItems(loc_main): %v", err)
	}

	else_ := findLow(items, "e27-else", "")
	if else_ == nil {
		t.Fatalf("item stocked only at loc_back must be on loc_main's reorder list, got %+v", items)
	}
	if !else_.NotStockedHere || else_.CurrentQty != 0 || else_.LocationID != "loc_main" || else_.LocationName != "Main Store" {
		t.Fatalf("elsewhere row: %+v, want NotStockedHere, qty 0, attributed to loc_main/Main Store", else_)
	}

	low := findLow(items, "e27-low", "")
	if low == nil || low.NotStockedHere || low.CurrentQty != 3 || low.LocationID != "loc_main" {
		t.Fatalf("item low at loc_main: %+v, want listed as today (qty 3, not flagged)", low)
	}

	if u := findLow(items, "e27-untr", ""); u != nil {
		t.Fatalf("an untracked item never appears on a reorder list, got %+v", u)
	}

	v := findLow(items, "e27-var", "e27-var-s")
	if v == nil || !v.NotStockedHere || v.CurrentQty != 0 || v.LocationID != "loc_main" {
		t.Fatalf("variant stocked only elsewhere: %+v, want listed at loc_main, qty 0, NotStockedHere", v)
	}
	if p := findLow(items, "e27-var", ""); p != nil {
		t.Fatalf("variant-tracked parent must not get a phantom item-scoped row, got %+v", p)
	}

	// Exactly one row per item/variant, however many other locations stock it.
	mustExec(t, d, `INSERT INTO stock_locations (id, name) VALUES ('loc_e27', 'Third')`)
	seedB8Inventory(t, d, "e27-else", "loc_e27", 7)
	items, err = repo.GetLowStockItems(ctx, "loc_main")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, it := range items {
		if it.ItemID == "e27-else" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("e27-else stocked at two other locations: got %d rows, want 1 (%+v)", n, items)
	}

	// The unfiltered list is unchanged: rows are real stock rows, never flagged.
	all, err := repo.GetLowStockItems(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range all {
		if it.NotStockedHere {
			t.Fatalf("unfiltered list must never flag a row not-stocked-here: %+v", it)
		}
	}

	// An unknown location has no reorder list of its own.
	if none, err := repo.GetLowStockItems(ctx, "loc-nope"); err != nil || findLow(none, "e27-else", "") != nil {
		t.Fatalf("unknown location must not list elsewhere-stocked items: %+v err=%v", none, err)
	}
}

// TestLowStockItemsFor_ShopUntracked: when the shop sells without tracking
// stock (pos.allow_negative_inventory), an item that isn't stocked here
// isn't something to reorder here — only real below-level rows remain.
func TestLowStockItemsFor_ShopUntracked(t *testing.T) {
	d, repo := openB8InvDB(t)
	ctx := context.Background()

	seedB8Item(t, d, "e27-else", "sku-else", "E27A Elsewhere", 5, 1)
	seedB8Inventory(t, d, "e27-else", "loc_back", 40)
	seedB8Item(t, d, "e27-low", "sku-low", "E27B LowHere", 10, 1)
	seedB8Inventory(t, d, "e27-low", "loc_main", 3)

	// Tracked shop: the not-stocked-here row is listed…
	tracked, err := repo.LowStockItemsFor(ctx, "loc_main", false)
	if err != nil {
		t.Fatal(err)
	}
	if e := findLow(tracked, "e27-else", ""); e == nil || !e.NotStockedHere {
		t.Fatalf("shop tracks stock: elsewhere-only item must be listed not stocked here, got %+v", tracked)
	}

	// …and hidden once the shop sells without tracking stock.
	items, err := repo.LowStockItemsFor(ctx, "loc_main", true)
	if err != nil {
		t.Fatal(err)
	}
	if findLow(items, "e27-else", "") != nil {
		t.Fatalf("shop sells without tracking stock: not-stocked-here rows must be hidden, got %+v", items)
	}
	if findLow(items, "e27-low", "") == nil {
		t.Fatalf("an item stocked here below its level stays listed, got %+v", items)
	}
}
