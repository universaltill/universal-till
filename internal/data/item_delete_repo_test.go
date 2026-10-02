package data_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#3317: an item that was never sold and is not in use is deleted
// for good; any sign of use refuses with a reason the owner can act on
// ("deactivate it instead"). The till's own data is what decides — never a
// flag the cloud sent along.

func seedDeletableItem(x func(string, ...any), id string) {
	x(`INSERT INTO items (id, sku, name, base_price, is_active) VALUES (?, ?, ?, 250, 1)`, id, "SKU-"+id, "Item "+id)
}

func itemExists(t *testing.T, db *sql.DB, id string) bool {
	t.Helper()
	ok, err := data.NewCatalogRepo(db).ItemExists(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func TestDeleteUnusedItem_DeletesNeverSoldItemAndItsChildren(t *testing.T) {
	d, x, _ := resetTestDB(t, "delete-item.db")
	repo := data.NewPOSRepo(d.DB)
	ctx := context.Background()
	seedDeletableItem(x, "del-1")
	x(`INSERT INTO item_barcodes (barcode, item_id, is_primary) VALUES ('2000000000011', 'del-1', 1)`)
	x(`INSERT OR IGNORE INTO stock_locations (id, name) VALUES ('loc_main', 'Main')`)
	x(`INSERT INTO inventory (id, item_id, location_id, quantity) VALUES ('inv-del-1', 'del-1', 'loc_main', 0)`)
	x(`INSERT INTO price_history (id, item_id, price) VALUES ('ph-del-1', 'del-1', 250)`)
	x(`INSERT INTO item_variants (id, item_id, sku, name, price, is_active) VALUES ('del-1-v', 'del-1', 'SKU-del-1-L', 'Large', 300, 1)`)

	res, err := repo.DeleteUnusedItem(ctx, "del-1")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if res.AlreadyDeleted || res.Name != "Item del-1" {
		t.Fatalf("result = %+v", res)
	}
	if itemExists(t, d.DB, "del-1") {
		t.Fatal("item still exists")
	}
	for _, q := range []string{
		`SELECT COUNT(*) FROM item_barcodes WHERE item_id = 'del-1'`,
		`SELECT COUNT(*) FROM inventory WHERE item_id = 'del-1'`,
		`SELECT COUNT(*) FROM price_history WHERE item_id = 'del-1'`,
		`SELECT COUNT(*) FROM item_variants WHERE item_id = 'del-1'`,
	} {
		var n int
		if err := d.DB.QueryRow(q).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s = %d (%v), want 0", q, n, err)
		}
	}
	// Re-applying (a lost result post makes the till apply again) is a
	// success that says so, not an error.
	res, err = repo.DeleteUnusedItem(ctx, "del-1")
	if err != nil || !res.AlreadyDeleted {
		t.Fatalf("replay: %+v %v", res, err)
	}
}

func TestDeleteUnusedItem_RefusesEachKindOfUse(t *testing.T) {
	cases := []struct {
		name   string
		seed   func(x func(string, ...any))
		reason string
		sales  int64
		msg    string
	}{
		{
			name: "sold twice",
			seed: func(x func(string, ...any)) {
				for _, s := range []string{"s-1", "s-2"} {
					x(`INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at) VALUES (?, ?, 'completed', 'sale', 'GBP', 250, 0, 0, 250, datetime('now'))`, s, "R-"+s)
					x(`INSERT INTO sale_lines (id, sale_id, line_no, item_id, name_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax) VALUES (?, ?, 1, 'use-1', 'Item', 1, 250, 0, 0, 0, 250, 250)`, "l-"+s, s)
				}
			},
			reason: data.ItemUseSold, sales: 2, msg: "sold 2 times — deactivate it instead",
		},
		{
			name: "sold once as a variant",
			seed: func(x func(string, ...any)) {
				x(`INSERT INTO item_variants (id, item_id, sku, name, price, is_active) VALUES ('use-1-v', 'use-1', 'SKU-use-1-L', 'Large', 300, 1)`)
				x(`INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at) VALUES ('s-v', 'R-v', 'completed', 'sale', 'GBP', 300, 0, 0, 300, datetime('now'))`)
				x(`INSERT INTO sale_lines (id, sale_id, line_no, variant_id, name_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax) VALUES ('l-v', 's-v', 1, 'use-1-v', 'Item L', 1, 300, 0, 0, 0, 300, 300)`)
			},
			reason: data.ItemUseSold, sales: 1, msg: "sold once — deactivate it instead",
		},
		{
			name: "sold before a reset (archive)",
			seed: func(x func(string, ...any)) {
				x(`INSERT INTO reset_batches (id, created_at) VALUES ('rb-1', datetime('now'))`)
				x(`INSERT INTO sale_lines_archive (id, sale_id, line_no, item_id, name_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax, reset_batch_id) VALUES ('la-1', 'sa-1', 1, 'use-1', 'Item', 1, 250, 0, 0, 0, 250, 250, 'rb-1')`)
			},
			reason: data.ItemUseSold, sales: 1, msg: "sold once — deactivate it instead",
		},
		{
			name: "on a quick button",
			seed: func(x func(string, ...any)) {
				x(`INSERT INTO shortcut_buttons (barcode, item_id, label, sort_order) VALUES ('QB-use-1', 'use-1', '', 0)`)
			},
			reason: data.ItemUseQuickButton, msg: "it is on a quick button — deactivate it instead",
		},
		{
			name: "in a parked sale",
			seed: func(x func(string, ...any)) {
				x(`INSERT INTO held_sales (id, payload) VALUES ('h-1', '{"lines":[{"item_id":"use-1","qty":1}]}')`)
			},
			reason: data.ItemUseOpenBasket, msg: "it is in an open basket — deactivate it instead",
		},
		{
			name: "stock history",
			seed: func(x func(string, ...any)) {
				x(`INSERT OR IGNORE INTO stock_locations (id, name) VALUES ('loc_main', 'Main')`)
				x(`INSERT INTO stock_movements (id, item_id, location_id, type, quantity) VALUES ('sm-1', 'use-1', 'loc_main', 'receive', 5)`)
			},
			reason: data.ItemUseStockHistory, msg: "it has stock history — deactivate it instead",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, x, _ := resetTestDB(t, "delete-item-refuse.db")
			repo := data.NewPOSRepo(d.DB)
			seedDeletableItem(x, "use-1")
			c.seed(x)

			_, err := repo.DeleteUnusedItem(context.Background(), "use-1")
			var inUse *data.ItemInUseError
			if !errors.As(err, &inUse) {
				t.Fatalf("err = %v, want *ItemInUseError", err)
			}
			if inUse.Reason != c.reason || inUse.Sales != c.sales || err.Error() != c.msg {
				t.Fatalf("got %+v %q, want %s/%d %q", inUse, err.Error(), c.reason, c.sales, c.msg)
			}
			if !itemExists(t, d.DB, "use-1") {
				t.Fatal("a refused delete removed the item")
			}
		})
	}
}

func TestEverSoldItemIDs(t *testing.T) {
	d, x, _ := resetTestDB(t, "ever-sold.db")
	seedDeletableItem(x, "es-direct")
	seedDeletableItem(x, "es-variant")
	seedDeletableItem(x, "es-archive")
	seedDeletableItem(x, "es-never")
	x(`INSERT INTO item_variants (id, item_id, sku, name, price, is_active) VALUES ('es-variant-v', 'es-variant', 'SKU-esv-L', 'Large', 300, 1)`)
	x(`INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at) VALUES ('s-es', 'R-es', 'completed', 'sale', 'GBP', 550, 0, 0, 550, datetime('now'))`)
	x(`INSERT INTO sale_lines (id, sale_id, line_no, item_id, name_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax) VALUES ('l-es-1', 's-es', 1, 'es-direct', 'A', 1, 250, 0, 0, 0, 250, 250)`)
	x(`INSERT INTO sale_lines (id, sale_id, line_no, variant_id, name_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax) VALUES ('l-es-2', 's-es', 2, 'es-variant-v', 'B', 1, 300, 0, 0, 0, 300, 300)`)
	x(`INSERT INTO reset_batches (id, created_at) VALUES ('rb-es', datetime('now'))`)
	x(`INSERT INTO sale_lines_archive (id, sale_id, line_no, item_id, name_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax, reset_batch_id) VALUES ('la-es', 'sa-es', 1, 'es-archive', 'C', 1, 250, 0, 0, 0, 250, 250, 'rb-es')`)

	got, err := data.NewPOSRepo(d.DB).EverSoldItemIDs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"es-direct", "es-variant", "es-archive"} {
		if !got[id] {
			t.Errorf("%s not reported as sold: %v", id, got)
		}
	}
	if got["es-never"] {
		t.Error("a never-sold item reported as sold")
	}
}
