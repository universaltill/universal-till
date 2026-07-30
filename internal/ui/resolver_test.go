package ui

import (
	"database/sql"
	"testing"
)

// seedResolverFixture builds a small catalog exercising every rung of the
// resolver fallthrough: a variant barcode, an item barcode, a
// shortcut-only barcode, a SKU, and a name.
func seedResolverFixture(t *testing.T) (*ButtonStore, *sql.DB) {
	t.Helper()
	db := setupFullTestDB(t)
	t.Cleanup(func() { db.Close() })

	mustExec(t, db, `INSERT INTO tax_codes(id, rate_basis_points) VALUES('tax-std', 2000)`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, tax_code_id, is_active, is_weighed) VALUES('itm-cof','COF-SKU','Coffee', 300, 'tax-std', 1, 0)`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active, is_weighed) VALUES('itm-ban','BAN-SKU','Bananas', 89, 1, 1)`)
	mustExec(t, db, `INSERT INTO item_images(id, item_id, role, path) VALUES('img-cof','itm-cof','thumbnail','/public/images/coffee.png')`)

	// Variant: Coffee -> Large, own barcode + own price.
	mustExec(t, db, `INSERT INTO item_variants(id, item_id, name, price, is_active) VALUES('var-lg','itm-cof','Large', 400, 1)`)
	mustExec(t, db, `INSERT INTO variant_barcodes(barcode, variant_id, is_primary) VALUES('VB-1','var-lg',1)`)

	// Item barcode for Coffee itself.
	mustExec(t, db, `INSERT INTO item_barcodes(barcode, item_id, is_primary) VALUES('IB-1','itm-cof',1)`)

	// Shortcut-only barcode (a Designer tile code that is NOT a real
	// item/variant barcode) pointing at Bananas, with a label override.
	mustExec(t, db, `INSERT INTO shortcut_buttons(barcode,label,item_id,sort_order) VALUES('SC-1','Loose Bananas','itm-ban',0)`)

	return NewButtonStore(db), db
}

func TestResolve_VariantBarcode(t *testing.T) {
	store, _ := seedResolverFixture(t)
	r := PriceResolverAdapter{Store: store}

	line, ok := r.Resolve("VB-1")
	if !ok {
		t.Fatalf("variant barcode did not resolve")
	}
	if line.VariantID != "var-lg" || line.ItemID != "itm-cof" {
		t.Fatalf("unexpected ids: %+v", line)
	}
	if line.Name != "Coffee - Large" {
		t.Fatalf("variant name = %q, want \"Coffee - Large\"", line.Name)
	}
	if line.PriceCents.Minor() != 400 {
		t.Fatalf("variant price = %d, want 400", line.PriceCents.Minor())
	}
	if line.TaxRateBP != 2000 || line.TaxCodeID != "tax-std" {
		t.Fatalf("tax not carried: %+v", line)
	}
	if line.ImageURL != "/public/images/coffee.png" {
		t.Fatalf("image not carried: %q", line.ImageURL)
	}
	if line.Qty != 1 {
		t.Fatalf("qty = %v, want 1", line.Qty)
	}
}

func TestResolve_ItemBarcode(t *testing.T) {
	store, _ := seedResolverFixture(t)
	r := PriceResolverAdapter{Store: store}

	line, ok := r.Resolve("IB-1")
	if !ok {
		t.Fatalf("item barcode did not resolve")
	}
	if line.ItemID != "itm-cof" || line.VariantID != "" {
		t.Fatalf("unexpected ids: %+v", line)
	}
	if line.PriceCents.Minor() != 300 || line.Name != "Coffee" {
		t.Fatalf("unexpected line: %+v", line)
	}
}

func TestResolve_ShortcutBarcodeUsesLabelAndWeighedFlag(t *testing.T) {
	store, _ := seedResolverFixture(t)
	r := PriceResolverAdapter{Store: store}

	line, ok := r.Resolve("SC-1")
	if !ok {
		t.Fatalf("shortcut barcode did not resolve")
	}
	if line.ItemID != "itm-ban" {
		t.Fatalf("unexpected item: %+v", line)
	}
	if line.Name != "Loose Bananas" {
		t.Fatalf("shortcut label override lost: %q", line.Name)
	}
	if !line.IsWeighed {
		t.Fatalf("weighed flag lost: %+v", line)
	}
	if line.PriceCents.Minor() != 89 {
		t.Fatalf("price = %d, want 89", line.PriceCents.Minor())
	}
}

func TestResolve_SKUAndNameFallback(t *testing.T) {
	store, _ := seedResolverFixture(t)
	r := PriceResolverAdapter{Store: store}

	// SKU exact.
	line, ok := r.Resolve("BAN-SKU")
	if !ok || line.ItemID != "itm-ban" {
		t.Fatalf("sku resolve failed: ok=%v line=%+v", ok, line)
	}

	// Name substring (cashier types part of the name).
	line, ok = r.Resolve("Banan")
	if !ok || line.ItemID != "itm-ban" {
		t.Fatalf("name resolve failed: ok=%v line=%+v", ok, line)
	}
}

func TestResolve_PriceHistoryOverridesBase(t *testing.T) {
	store, db := seedResolverFixture(t)
	mustExec(t, db, `INSERT INTO price_history(id, item_id, price, starts_at) VALUES('ph1','itm-cof', 275, datetime('now','-1 hour'))`)
	r := PriceResolverAdapter{Store: store}

	line, ok := r.Resolve("IB-1")
	if !ok {
		t.Fatalf("resolve failed")
	}
	if line.PriceCents.Minor() != 275 {
		t.Fatalf("price = %d, want current history price 275", line.PriceCents.Minor())
	}
}

func TestResolve_MissAndEmpty(t *testing.T) {
	store, _ := seedResolverFixture(t)
	r := PriceResolverAdapter{Store: store}

	if _, ok := r.Resolve("no-such-code"); ok {
		t.Fatalf("expected miss for unknown code")
	}
	if _, ok := r.Resolve("   "); ok {
		t.Fatalf("expected miss for blank code")
	}
}

func TestResolve_InactiveItemDoesNotResolve(t *testing.T) {
	store, db := seedResolverFixture(t)
	mustExec(t, db, `UPDATE items SET is_active = 0 WHERE id = 'itm-ban'`)
	r := PriceResolverAdapter{Store: store}

	for _, code := range []string{"SC-1", "BAN-SKU", "Banan"} {
		if _, ok := r.Resolve(code); ok {
			t.Fatalf("inactive item resolved via %q", code)
		}
	}
}
