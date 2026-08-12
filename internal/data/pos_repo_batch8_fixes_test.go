package data

import (
	"context"
	"testing"
	"time"
)

// Coverage batch 8 surfaced two real POSRepo bugs; these are their
// regression tests, written failing-first against the unfixed code.
//
// 1. GetLowStockItems: an item with reorder_level > 0 but no inventory
//    row yet made the WHOLE query fail (NULL inv.location_id scanned
//    into a plain string), so one never-stocked item silently killed
//    every low-stock alert (the back-office caller drops the error).
// 2. MarginByItem: doc-intent is "variant's cost when present, else the
//    item's", but items were joined on sl.item_id, which is NULL for
//    variant lines — a variant without its own cost_price vanished from
//    the margin report even when its parent item had a cost.

func TestGetLowStockItems_NeverStockedItemDoesNotPoisonReport(t *testing.T) {
	d := b8OpenDB(t, "lowstock-fix.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	// Neutralize the demo seed catalog so the assertion set is exact.
	mustExec(t, d, `DELETE FROM inventory`)
	mustExec(t, d, `UPDATE items SET reorder_level = 0`)
	mustExec(t, d, `INSERT INTO stock_locations (id, name) VALUES ('loc_fix', 'Fix Main')`)

	b8Item(t, d, "fix-stocked", 500, nil, 1)
	mustExec(t, d, `UPDATE items SET reorder_level = 5 WHERE id = 'fix-stocked'`)
	mustExec(t, d, `INSERT INTO inventory (id, item_id, location_id, quantity) VALUES ('inv-fix', 'fix-stocked', 'loc_fix', 2)`)

	// The poison pill: reorder level set, but no inventory row yet —
	// a brand-new item a shop hasn't received stock for.
	b8Item(t, d, "fix-never", 500, nil, 1)
	mustExec(t, d, `UPDATE items SET reorder_level = 5 WHERE id = 'fix-never'`)

	items, err := repo.GetLowStockItems(ctx, "")
	if err != nil {
		t.Fatalf("GetLowStockItems must survive a never-stocked item, got: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("want 2 low-stock rows (stocked-below-level + never-stocked), got %d: %+v", len(items), items)
	}
	// ORDER BY i.name: "Name fix-never" < "Name fix-stocked".
	never, stocked := items[0], items[1]
	if never.ItemID != "fix-never" || stocked.ItemID != "fix-stocked" {
		t.Fatalf("wrong rows/order: %+v", items)
	}
	if never.CurrentQty != 0 || never.LocationID != "" || never.LocationName != "" {
		t.Errorf("never-stocked row must report qty 0 with empty location, got %+v", never)
	}
	if stocked.CurrentQty != 2 || stocked.LocationID != "loc_fix" || stocked.LocationName != "Fix Main" || stocked.ReorderLevel != 5 {
		t.Errorf("stocked row fields wrong: %+v", stocked)
	}
}

func TestMarginByItem_VariantWithoutOwnCostFallsBackToItemCost(t *testing.T) {
	d := b8OpenDB(t, "margin-fix.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	b8Item(t, d, "fix-parent", 300, int64(100), 1)
	mustExec(t, d, `INSERT INTO item_variants (id, item_id, name, price) VALUES ('v-fix', 'fix-parent', 'Large', 300)`)

	now := b8At(time.Now().Add(-1 * time.Hour))
	b8Sale(t, d, "sale-fix", now, "completed", "sale", 0, 600)
	b8Line(t, d, "sale-fix", 1, "", "v-fix", "Fix Parent Large", 2, 0, 0, 600, 600)

	start, end := lastDays(30)
	rows, err := repo.MarginByItem(ctx, start, end, 10)
	if err != nil {
		t.Fatal(err)
	}
	var got *MarginRow
	for i := range rows {
		if rows[i].Name == "Fix Parent Large" {
			got = &rows[i]
		}
	}
	if got == nil {
		t.Fatalf("variant line (no variant cost, parent item cost 100) missing from margin report: %+v", rows)
	}
	if got.Revenue != 600 || got.Cost != 200 || got.Margin != 400 {
		t.Errorf("want revenue 600 / cost 2x100=200 / margin 400, got %+v", *got)
	}
}
