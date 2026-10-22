package data_test

import (
	"context"
	"errors"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#4746: a void on a never-sold item is not sales history. The
// delete detaches the live void rows (the Shrinkage report keeps its
// totals through item_name) and reports how many; real history still
// refuses with a reason that names it.

func seedShrinkage(x func(string, ...any), id, reason, itemID string) {
	x(`INSERT INTO shrinkage_events (id, reason_category, item_id, item_name, quantity, unit_price_minor, extended_value_minor, created_at)
	   VALUES (?, ?, ?, 'Item', 1, 1000, 1000, '2026-10-21T21:42:44Z')`, id, reason, itemID)
}

func TestDeleteUnusedItem_VoidOnlyItemIsDeletedAndVoidsDetached(t *testing.T) {
	d, x, _ := resetTestDB(t, "delete-item-voids.db")
	repo := data.NewPOSRepo(d.DB)
	seedDeletableItem(x, "void-1")
	seedShrinkage(x, "se-1", "void", "void-1")
	seedShrinkage(x, "se-2", "void", "void-1")
	x(`INSERT OR IGNORE INTO stock_locations (id, name) VALUES ('loc_main', 'Main')`)
	x(`INSERT INTO stock_movements (id, item_id, location_id, type, quantity) VALUES ('sm-zero', 'void-1', 'loc_main', 'adjust', 0)`)

	res, err := repo.DeleteUnusedItem(context.Background(), "void-1")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if res.Voids != 2 {
		t.Fatalf("Voids = %d, want 2", res.Voids)
	}
	if itemExists(t, d.DB, "void-1") {
		t.Fatal("item still exists")
	}
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM shrinkage_events WHERE item_id IS NULL AND item_name = 'Item'`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("detached void rows = %d (%v), want 2 kept with their name", n, err)
	}
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM stock_movements WHERE id = 'sm-zero'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("zero movement rows = %d (%v), want 0", n, err)
	}
}

func TestDeleteUnusedItem_RefusalsNameTheRealHistory(t *testing.T) {
	cases := []struct {
		name   string
		seed   func(x func(string, ...any))
		reason string
		msg    string
	}{
		{
			name:   "comped",
			seed:   func(x func(string, ...any)) { seedShrinkage(x, "se-c", "comp", "use-2") },
			reason: data.ItemUseShrinkage, msg: "it was comped or wasted 1 time — deactivate it instead",
		},
		{
			name: "wasted twice",
			seed: func(x func(string, ...any)) {
				seedShrinkage(x, "se-w1", "waste", "use-2")
				seedShrinkage(x, "se-w2", "waste", "use-2")
			},
			reason: data.ItemUseShrinkage, msg: "it was comped or wasted 2 times — deactivate it instead",
		},
		{
			name: "void kept by a restorable reset",
			seed: func(x func(string, ...any)) {
				x(`INSERT INTO reset_batches (id, created_at) VALUES ('rb-2', datetime('now'))`)
				x(`INSERT INTO shrinkage_events_archive (id, reason_category, item_id, item_name, quantity, unit_price_minor, extended_value_minor, created_at, reset_batch_id)
				   VALUES ('sea-1', 'void', 'use-2', 'Item', 1, 1000, 1000, '2026-10-21T21:42:44Z', 'rb-2')`)
			},
			reason: data.ItemUseArchivedShrinkage, msg: "its voids sit in an archived reset that can still be restored — deactivate it instead",
		},
		{
			name: "real stock movement",
			seed: func(x func(string, ...any)) {
				x(`INSERT OR IGNORE INTO stock_locations (id, name) VALUES ('loc_main', 'Main')`)
				x(`INSERT INTO stock_movements (id, item_id, location_id, type, quantity) VALUES ('sm-2', 'use-2', 'loc_main', 'receive', 5)`)
			},
			reason: data.ItemUseStockHistory, msg: "it has stock history — deactivate it instead",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, x, _ := resetTestDB(t, "delete-item-named.db")
			seedDeletableItem(x, "use-2")
			c.seed(x)
			_, err := data.NewPOSRepo(d.DB).DeleteUnusedItem(context.Background(), "use-2")
			var inUse *data.ItemInUseError
			if !errors.As(err, &inUse) || inUse.Reason != c.reason || err.Error() != c.msg {
				t.Fatalf("err = %v (%+v), want %s %q", err, inUse, c.reason, c.msg)
			}
			if !itemExists(t, d.DB, "use-2") {
				t.Fatal("a refused delete removed the item")
			}
			// A refusal changes nothing: voids stay attached.
			var n int
			_ = d.DB.QueryRow(`SELECT COUNT(*) FROM shrinkage_events WHERE item_id = 'use-2'`).Scan(&n)
			if c.reason == data.ItemUseShrinkage && n == 0 {
				t.Fatal("a refused delete detached shrinkage rows")
			}
		})
	}
}

func TestDeleteUnusedItem_SoldItemWithVoidsStillRefusesAsSold(t *testing.T) {
	d, x, _ := resetTestDB(t, "delete-item-sold-voids.db")
	seedDeletableItem(x, "sv-1")
	seedShrinkage(x, "se-sv", "void", "sv-1")
	x(`INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at) VALUES ('s-sv', 'R-sv', 'completed', 'sale', 'GBP', 250, 0, 0, 250, datetime('now'))`)
	x(`INSERT INTO sale_lines (id, sale_id, line_no, item_id, name_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax) VALUES ('l-sv', 's-sv', 1, 'sv-1', 'Item', 1, 250, 0, 0, 0, 250, 250)`)
	_, err := data.NewPOSRepo(d.DB).DeleteUnusedItem(context.Background(), "sv-1")
	var inUse *data.ItemInUseError
	if !errors.As(err, &inUse) || inUse.Reason != data.ItemUseSold {
		t.Fatalf("err = %v, want sold", err)
	}
	var n int
	_ = d.DB.QueryRow(`SELECT COUNT(*) FROM shrinkage_events WHERE item_id = 'sv-1'`).Scan(&n)
	if n != 1 {
		t.Fatal("a refused delete detached the void")
	}
}
