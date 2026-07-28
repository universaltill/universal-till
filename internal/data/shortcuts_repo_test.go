package data_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
)

// A shortcut button (sale-screen product tile) has its own, separate
// image_path column from item_images (the catalog's own image storage,
// also used by the barcode/scan resolvers) — nothing ever populated it from
// a plain catalog image upload, so an item's catalog photo showed up in the
// catalog list but never on its actual sale-screen tile. Confirmed live
// 2026-07-29.
func TestLoadButtons_FallsBackToCatalogImage(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "shortcuts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	ctx := context.Background()
	ex := func(q string, args ...any) {
		if _, err := d.DB.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}

	// item-a: catalog image only, no button-specific image — must fall back.
	ex(`INSERT INTO items (id, sku, name, base_price, is_active, is_weighed, unit) VALUES ('item-a','SKU-A','Latte',320,1,0,'each')`)
	ex(`INSERT INTO item_images (id, item_id, path, role) VALUES ('img-a','item-a','/public/assets/items/item-a/thumb.png','thumbnail')`)
	ex(`INSERT INTO shortcut_buttons (barcode, item_id, label, sort_order) VALUES ('BTN-A','item-a','Latte',1)`)

	// item-b: an explicit button image set — must win over the catalog image.
	ex(`INSERT INTO items (id, sku, name, base_price, is_active, is_weighed, unit) VALUES ('item-b','SKU-B','Cappuccino',320,1,0,'each')`)
	ex(`INSERT INTO item_images (id, item_id, path, role) VALUES ('img-b','item-b','/public/assets/items/item-b/thumb.png','thumbnail')`)
	ex(`INSERT INTO shortcut_buttons (barcode, item_id, label, image_path, sort_order) VALUES ('BTN-B','item-b','Cappuccino','/public/images/custom-cappuccino.png',2)`)

	// item-c: neither — must stay empty, not error.
	ex(`INSERT INTO items (id, sku, name, base_price, is_active, is_weighed, unit) VALUES ('item-c','SKU-C','Croissant',195,1,0,'each')`)
	ex(`INSERT INTO shortcut_buttons (barcode, item_id, label, sort_order) VALUES ('BTN-C','item-c','Croissant',3)`)

	repo := data.NewShortcutsRepo(d.DB)
	btns, err := repo.LoadButtons(ctx)
	if err != nil {
		t.Fatalf("LoadButtons: %v", err)
	}

	byLabel := map[string]data.ShortcutButton{}
	for _, b := range btns {
		byLabel[b.Label] = b
	}
	for _, want := range []string{"Latte", "Cappuccino", "Croissant"} {
		if _, ok := byLabel[want]; !ok {
			t.Fatalf("expected a button labeled %q among the loaded buttons", want)
		}
	}

	if got := byLabel["Latte"].ImageURL; got != "/public/assets/items/item-a/thumb.png" {
		t.Fatalf("expected catalog-image fallback for Latte, got %q", got)
	}
	if got := byLabel["Cappuccino"].ImageURL; got != "/public/images/custom-cappuccino.png" {
		t.Fatalf("expected the explicit button image to win for Cappuccino, got %q", got)
	}
	if got := byLabel["Croissant"].ImageURL; got != "" {
		t.Fatalf("expected no image for Croissant, got %q", got)
	}
}
