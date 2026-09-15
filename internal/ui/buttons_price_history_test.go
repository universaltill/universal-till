package ui

// ut-docs#2258: the sale-screen shortcut grid must show each tile's
// CURRENT effective price — an active price_history row when one exists,
// else the item's configured base_price — never the raw base_price alone.
// Mirrors TestButtonStoreLoad_FlagsVariants's pattern, and is the
// item-level counterpart to ut-docs#2228's variant-picker fix.

import (
	"testing"
)

func TestButtonStoreLoad_ShowsCurrentPriceHistoryPrice(t *testing.T) {
	db := setupFullTestDB(t)
	defer db.Close()

	// itm-promo has an active promotional price_history row (250 instead
	// of its configured 310) — the exact shape ut-docs#2228 fixed for the
	// variant picker, now checked for the tile.
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm-promo','S1','Cola', 310, 1)`)
	// itm-plain has no price_history row at all — must fall back to base_price.
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm-plain','S2','Water', 100, 1)`)
	// itm-expired has an EXPIRED price_history row — must NOT apply.
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm-expired','S3','Juice', 250, 1)`)

	mustExec(t, db, `INSERT INTO price_history(id, item_id, price, starts_at) VALUES('ph1','itm-promo',250, datetime('now','-1 hour'))`)
	mustExec(t, db, `INSERT INTO price_history(id, item_id, price, starts_at, ends_at) VALUES('ph2','itm-expired',999, datetime('now','-2 day'), datetime('now','-1 day'))`)

	mustExec(t, db, `INSERT INTO shortcut_buttons(barcode,label,item_id,sort_order) VALUES('C1','Cola Tile','itm-promo',0)`)
	mustExec(t, db, `INSERT INTO shortcut_buttons(barcode,label,item_id,sort_order) VALUES('W1','Water Tile','itm-plain',1)`)
	mustExec(t, db, `INSERT INTO shortcut_buttons(barcode,label,item_id,sort_order) VALUES('J1','Juice Tile','itm-expired',2)`)

	store := NewButtonStore(db)
	btns, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(btns) != 3 {
		t.Fatalf("len = %d, want 3", len(btns))
	}
	byLabel := map[string]Button{}
	for _, b := range btns {
		byLabel[b.Label] = b
	}
	if byLabel["Cola Tile"].Price != 250 {
		t.Fatalf("expected Cola tile to show the active price_history override 250, got %d", byLabel["Cola Tile"].Price)
	}
	if byLabel["Water Tile"].Price != 100 {
		t.Fatalf("expected Water tile to show its configured price 100 (no price_history row), got %d", byLabel["Water Tile"].Price)
	}
	if byLabel["Juice Tile"].Price != 250 {
		t.Fatalf("expected Juice tile to show its configured price 250 (price_history row EXPIRED), got %d", byLabel["Juice Tile"].Price)
	}
}
