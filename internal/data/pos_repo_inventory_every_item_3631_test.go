package data

import (
	"context"
	"testing"
)

// ut-docs#3631: the /inventory list must show every active, stock-tracked
// item (and every active variant of one), so the page-head "+" button could
// go — an item never received yet is listed at qty 0 at the Main location,
// and the operator taps its row to receive/adjust.

func findLevels(levels []LowStockItem, itemID, variantID string) []LowStockItem {
	var out []LowStockItem
	for _, l := range levels {
		if l.ItemID == itemID && l.VariantID == variantID {
			out = append(out, l)
		}
	}
	return out
}

func TestListStockLevelsIncludingUnstocked_UnstockedItemListedAtMain(t *testing.T) {
	d, repo := openB8InvDB(t)
	ctx := context.Background()

	seedB8Item(t, d, "e-new", "sku-e-new", "Every New", 4, 1)
	mustExec(t, d, `UPDATE items SET lead_time_days = 3, category_id = NULL WHERE id = 'e-new'`)

	levels, err := repo.ListStockLevelsIncludingUnstocked(ctx)
	if err != nil {
		t.Fatalf("ListStockLevelsIncludingUnstocked: %v", err)
	}
	got := findLevels(levels, "e-new", "")
	if len(got) != 1 {
		t.Fatalf("want exactly one zero row for the never-stocked item, got %+v", got)
	}
	r := got[0]
	if r.CurrentQty != 0 || r.LocationID != "loc_main" || r.LocationName != "Main Store" ||
		r.SKU != "sku-e-new" || r.Name != "Every New" || r.ReorderLevel != 4 || r.LeadTimeDays != 3 {
		t.Fatalf("zero row: %+v, want qty 0 at loc_main/Main Store carrying sku/reorder/lead time", r)
	}
}

func TestListStockLevelsIncludingUnstocked_ExcludesUntrackedAndInactive(t *testing.T) {
	d, repo := openB8InvDB(t)
	ctx := context.Background()

	seedB8Item(t, d, "e-untr", "sku-e-untr", "Every Untracked", 0, 1)
	mustExec(t, d, `UPDATE items SET stock_untracked = 1 WHERE id = 'e-untr'`)
	seedB8Item(t, d, "e-inact", "sku-e-inact", "Every Inactive", 0, 0)
	// An untracked parent's variant must not get a zero row either.
	seedB8Item(t, d, "e-untrv", "sku-e-untrv", "Every Untracked Var", 0, 1)
	mustExec(t, d, `INSERT INTO item_variants (id, item_id, sku, name, price) VALUES ('e-untrv-v1', 'e-untrv', 'sku-e-untrv-v1', 'S', 100)`)
	mustExec(t, d, `UPDATE items SET stock_untracked = 1 WHERE id = 'e-untrv'`)
	// An inactive variant of a tracked item is not listed.
	seedB8Item(t, d, "e-iv", "sku-e-iv", "Every Inactive Var", 0, 1)
	mustExec(t, d, `INSERT INTO item_variants (id, item_id, sku, name, price, is_active) VALUES ('e-iv-v1', 'e-iv', 'sku-e-iv-v1', 'Gone', 100, 0)`)

	levels, err := repo.ListStockLevelsIncludingUnstocked(ctx)
	if err != nil {
		t.Fatalf("ListStockLevelsIncludingUnstocked: %v", err)
	}
	for _, l := range levels {
		switch {
		case l.ItemID == "e-untr", l.ItemID == "e-inact", l.ItemID == "e-untrv":
			t.Fatalf("untracked/inactive item must not be listed: %+v", l)
		case l.VariantID == "e-iv-v1":
			t.Fatalf("inactive variant must not be listed: %+v", l)
		}
	}
	// Its only variant is inactive, so the item itself is stocked at item
	// level and gets the item-scoped zero row.
	if got := findLevels(levels, "e-iv", ""); len(got) != 1 {
		t.Fatalf("item whose only variant is inactive must get one item-level zero row, got %+v", got)
	}
}

func TestListStockLevelsIncludingUnstocked_NoDuplicateForStockedItem(t *testing.T) {
	d, repo := openB8InvDB(t)
	ctx := context.Background()

	seedB8Item(t, d, "e-stk", "sku-e-stk", "Every Stocked", 0, 1)
	// Stocked only at a non-Main location: still no extra Main zero row.
	seedB8Inventory(t, d, "e-stk", "loc_back", 7)

	levels, err := repo.ListStockLevelsIncludingUnstocked(ctx)
	if err != nil {
		t.Fatalf("ListStockLevelsIncludingUnstocked: %v", err)
	}
	got := findLevels(levels, "e-stk", "")
	if len(got) != 1 || got[0].CurrentQty != 7 || got[0].LocationID != "loc_back" {
		t.Fatalf("stocked item must appear once with its real row, got %+v", got)
	}
}

func TestListStockLevelsIncludingUnstocked_VariantsGetOwnZeroRowsNoParentRow(t *testing.T) {
	d, repo := openB8InvDB(t)
	ctx := context.Background()

	seedB8Item(t, d, "e-var", "sku-e-var", "Every Variants", 0, 1)
	mustExec(t, d, `INSERT INTO item_variants (id, item_id, sku, name, price) VALUES ('e-var-v1', 'e-var', 'sku-e-var-v1', 'Small', 100)`)
	mustExec(t, d, `INSERT INTO item_variants (id, item_id, sku, name, price) VALUES ('e-var-v2', 'e-var', 'sku-e-var-v2', 'Large', 120)`)
	mustExec(t, d, `INSERT INTO inventory (id, item_id, variant_id, location_id, quantity) VALUES ('inv-e-var-v2', NULL, 'e-var-v2', 'loc_back', 9)`)

	levels, err := repo.ListStockLevelsIncludingUnstocked(ctx)
	if err != nil {
		t.Fatalf("ListStockLevelsIncludingUnstocked: %v", err)
	}
	if got := findLevels(levels, "e-var", ""); len(got) != 0 {
		t.Fatalf("item with active variants is stocked per variant — no parent zero row, got %+v", got)
	}
	v1 := findLevels(levels, "e-var", "e-var-v1")
	if len(v1) != 1 || v1[0].CurrentQty != 0 || v1[0].LocationID != "loc_main" ||
		v1[0].VariantName != "Small" || v1[0].SKU != "sku-e-var-v1" {
		t.Fatalf("never-stocked variant must get one zero row at Main, got %+v", v1)
	}
	v2 := findLevels(levels, "e-var", "e-var-v2")
	if len(v2) != 1 || v2[0].CurrentQty != 9 || v2[0].LocationID != "loc_back" {
		t.Fatalf("stocked variant must appear once with its real row, got %+v", v2)
	}
}

func TestListStockLevelsIncludingUnstocked_NoMainLocationLeavesLocationEmpty(t *testing.T) {
	d, repo := openB8InvDB(t)
	ctx := context.Background()

	mustExec(t, d, `UPDATE stock_locations SET is_active = 0 WHERE id = 'loc_main'`)
	seedB8Item(t, d, "e-noloc", "sku-e-noloc", "Every No Main", 0, 1)

	levels, err := repo.ListStockLevelsIncludingUnstocked(ctx)
	if err != nil {
		t.Fatalf("ListStockLevelsIncludingUnstocked: %v", err)
	}
	got := findLevels(levels, "e-noloc", "")
	if len(got) != 1 || got[0].LocationID != "" || got[0].LocationName != "" || got[0].CurrentQty != 0 {
		t.Fatalf("with no active Main location the zero row has an empty location, got %+v", got)
	}
}

func TestListStockLevelsIncludingUnstocked_SortedByName(t *testing.T) {
	d, repo := openB8InvDB(t)
	ctx := context.Background()

	seedB8Item(t, d, "e-s2", "sku-e-s2", "Zz Every Sort", 0, 1)
	seedB8Item(t, d, "e-s1", "sku-e-s1", "aa Every Sort", 0, 1)
	seedB8Inventory(t, d, "e-s2", "loc_main", 1)

	levels, err := repo.ListStockLevelsIncludingUnstocked(ctx)
	if err != nil {
		t.Fatalf("ListStockLevelsIncludingUnstocked: %v", err)
	}
	i1, i2 := -1, -1
	for i, l := range levels {
		switch l.ItemID {
		case "e-s1":
			i1 = i
		case "e-s2":
			i2 = i
		}
	}
	if i1 < 0 || i2 < 0 || i1 > i2 {
		t.Fatalf("zero rows must be merged in name order (case-insensitive): aa@%d Zz@%d", i1, i2)
	}
}
