package data

import (
	"context"
	"sort"
	"strings"
	"testing"
)

// ut-docs#2765: sell_screen_version.generation is also the open sale
// screen's live-refresh signal (GET /ui/buttons/version, polled by
// web/public/sell-screen-watch.js). It must move on every write to a table
// the tile grid renders from — so another till's / the cloud's / a sync
// pull's catalog change reaches an already-open sale screen — and on NOTHING
// else, so a completed sale (sales, sale_lines, payments, inventory,
// stock_movements, audit_log, settings, …) never makes every open grid
// refetch.

// sellScreenGridTables is exactly the set of tables the /ui/buttons and
// /ui/buttons/category renders read (internal/ui/buttons.go's loadAllActive,
// loadWith, LoadCategories, LoadCategoriesForAdmin and the repo queries they
// call). 042 covered price_history and item_images; 047 adds the rest.
var sellScreenGridTables = []string{
	"categories",
	"category_modifier_group_links",
	"item_barcodes",
	"item_images",
	"item_modifier_group_links",
	"item_modifier_group_opt_outs",
	"item_modifier_groups",
	"item_variants",
	"items",
	"price_history",
	"shortcut_buttons",
	"translation_overrides",
	"variant_barcodes",
}

// Structural pin: every grid table has all three bump triggers, and no
// other table has any — a new trigger on sales/settings/inventory would
// make every completed sale refresh every open sale screen.
func TestSellScreenVersion_TriggersExactlyOnGridTables(t *testing.T) {
	d := openMigratedDB(t, "sellscreen-trigger-set.db")
	rows, err := d.Query(`SELECT tbl_name, sql FROM sqlite_master WHERE type = 'trigger'`)
	if err != nil {
		t.Fatalf("list triggers: %v", err)
	}
	defer rows.Close()
	events := map[string]map[string]bool{}
	for rows.Next() {
		var tbl, sqlText string
		if err := rows.Scan(&tbl, &sqlText); err != nil {
			t.Fatalf("scan trigger: %v", err)
		}
		if !strings.Contains(sqlText, "sell_screen_version") {
			continue
		}
		if events[tbl] == nil {
			events[tbl] = map[string]bool{}
		}
		up := strings.ToUpper(sqlText)
		for _, ev := range []string{"AFTER INSERT", "AFTER UPDATE", "AFTER DELETE"} {
			if strings.Contains(up, ev) {
				events[tbl][ev] = true
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate triggers: %v", err)
	}
	want := map[string]bool{}
	for _, tbl := range sellScreenGridTables {
		want[tbl] = true
		for _, ev := range []string{"AFTER INSERT", "AFTER UPDATE", "AFTER DELETE"} {
			if !events[tbl][ev] {
				t.Errorf("table %s has no %s trigger bumping sell_screen_version", tbl, ev)
			}
		}
	}
	var extra []string
	for tbl := range events {
		if !want[tbl] {
			extra = append(extra, tbl)
		}
	}
	sort.Strings(extra)
	if len(extra) > 0 {
		t.Errorf("sell_screen_version is bumped by non-grid table(s) %v — it must stay catalog-only (ut-docs#2765)", extra)
	}
}

func TestSellScreenVersion_CatalogWritesBump(t *testing.T) {
	d := openMigratedDB(t, "sellscreen-catalog-bump.db")
	steps := []struct{ name, sql string }{
		{"items insert", `INSERT INTO items (id, sku, name, base_price) VALUES ('itm1', 'COLA', 'Cola Can', 120)`},
		{"items update (rename)", `UPDATE items SET name = 'Cola' WHERE id = 'itm1'`},
		{"items deactivate", `UPDATE items SET is_active = 0 WHERE id = 'itm1'`},
		{"categories insert", `INSERT INTO categories (id, name) VALUES ('cat1', 'Drinks')`},
		{"categories update", `UPDATE categories SET name = 'Cold drinks' WHERE id = 'cat1'`},
		{"shortcut_buttons insert", `INSERT INTO shortcut_buttons (barcode, item_id, label) VALUES ('COLA', 'itm1', 'Cola')`},
		{"shortcut_buttons update", `UPDATE shortcut_buttons SET sort_order = 3 WHERE barcode = 'COLA'`},
		{"shortcut_buttons delete", `DELETE FROM shortcut_buttons WHERE barcode = 'COLA'`},
		{"item_barcodes insert", `INSERT INTO item_barcodes (barcode, item_id, is_primary) VALUES ('5000112637922', 'itm1', 1)`},
		{"item_barcodes update", `UPDATE item_barcodes SET is_primary = 0 WHERE barcode = '5000112637922'`},
		{"item_barcodes delete", `DELETE FROM item_barcodes WHERE barcode = '5000112637922'`},
		{"item_variants insert", `INSERT INTO item_variants (id, item_id, sku, name, price) VALUES ('v1', 'itm1', 'COLA-L', 'Large', 150)`},
		{"item_variants update", `UPDATE item_variants SET is_active = 0 WHERE id = 'v1'`},
		{"variant_barcodes insert", `INSERT INTO variant_barcodes (barcode, variant_id) VALUES ('VB1', 'v1')`},
		{"variant_barcodes delete", `DELETE FROM variant_barcodes WHERE barcode = 'VB1'`},
		{"item_variants delete", `DELETE FROM item_variants WHERE id = 'v1'`},
		{"item_modifier_groups insert", `INSERT INTO item_modifier_groups (id, name) VALUES ('g1', 'Milk')`},
		{"item_modifier_groups update", `UPDATE item_modifier_groups SET is_active = 0 WHERE id = 'g1'`},
		{"item_modifier_group_links insert", `INSERT INTO item_modifier_group_links (item_id, group_id) VALUES ('itm1', 'g1')`},
		{"item_modifier_group_links delete", `DELETE FROM item_modifier_group_links WHERE item_id = 'itm1'`},
		{"category_modifier_group_links insert", `INSERT INTO category_modifier_group_links (category_id, group_id) VALUES ('cat1', 'g1')`},
		{"item_modifier_group_opt_outs insert", `INSERT INTO item_modifier_group_opt_outs (item_id, group_id) VALUES ('itm1', 'g1')`},
		{"translation_overrides insert", `INSERT INTO translation_overrides (locale, key, value, updated_at) VALUES ('en', 'sale.title', 'Till', '2026-09-25T00:00:00Z')`},
		{"categories delete", `DELETE FROM categories WHERE id = 'cat1'`},
	}
	prev := sellScreenGeneration(t, d)
	for _, s := range steps {
		mustExec(t, d, s.sql)
		got := sellScreenGeneration(t, d)
		if got <= prev {
			t.Fatalf("%s: sell_screen_version %d -> %d, want it to move", s.name, prev, got)
		}
		prev = got
	}
}

// A write a completed sale makes — plus a settings write — must NOT move
// the counter (the behavioural half of the structural pin above; the real
// /api/pos/tender path is covered in internal/pages).
func TestSellScreenVersion_SaleAndSettingsWritesDoNotBump(t *testing.T) {
	d := openMigratedDB(t, "sellscreen-sale-no-bump.db")
	mustExec(t, d, `INSERT INTO items (id, sku, name, base_price) VALUES ('itm1', 'COLA', 'Cola Can', 120)`)
	before := sellScreenGeneration(t, d)
	mustExec(t, d, `INSERT INTO settings (key, value) VALUES ('ut2765.probe', '1')`)
	mustExec(t, d, `UPDATE settings SET value = '2' WHERE key = 'ut2765.probe'`)
	mustExec(t, d, `INSERT INTO inventory (id, item_id, location_id, quantity, updated_at) VALUES ('inv1', 'itm1', 'loc_main', 5, datetime('now'))`)
	mustExec(t, d, `UPDATE inventory SET quantity = 4 WHERE id = 'inv1'`)
	if after := sellScreenGeneration(t, d); after != before {
		t.Fatalf("settings/inventory writes moved sell_screen_version %d -> %d; it must stay catalog-only", before, after)
	}
}

func TestSellScreenRepo_SellGeneration(t *testing.T) {
	d := openMigratedDB(t, "sellscreen-sell-generation.db")
	repo := NewSellScreenRepo(d.DB)
	ctx := context.Background()
	g0, ok, err := repo.SellGeneration(ctx)
	if err != nil || !ok {
		t.Fatalf("SellGeneration on a migrated db = ok %v, err %v; want ok", ok, err)
	}
	mustExec(t, d, `INSERT INTO items (id, sku, name, base_price) VALUES ('itm1', 'COLA', 'Cola Can', 120)`)
	g1, _, _ := repo.SellGeneration(ctx)
	if g1 <= g0 {
		t.Fatalf("item insert: SellGeneration %d -> %d, want it to move", g0, g1)
	}
	_, sell, _, _ := repo.SellScreenVersion(ctx)
	if sell != g1 {
		t.Fatalf("SellGeneration %d disagrees with SellScreenVersion's sell half %d", g1, sell)
	}
	mustExec(t, d, `DELETE FROM sell_screen_version`)
	g, ok, err := repo.SellGeneration(ctx)
	if err != nil || ok || g != 0 {
		t.Fatalf("missing row: SellGeneration = %d, ok %v, err %v; want 0, false, nil", g, ok, err)
	}
}
