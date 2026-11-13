package data_test

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/iconid"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// A shortcut button (sale-screen product tile) has its own, separate
// image_path column from item_images (the catalog's own image storage,
// also used by the barcode/scan resolvers) — nothing ever populated it from
// a plain catalog image upload, so an item's catalog photo showed up in the
// catalog list but never on its actual sale-screen tile. Confirmed live
// 2026-07-29.
func TestLoadButtons_FallsBackToCatalogImage(t *testing.T) {
	d, err := db.Open(testsupport.MigratedDBFile(t, "shortcuts.db"))
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

	// item-b: an explicit button image AND a catalog photo — the photo wins
	// (ut-docs#4838); the button image is only the fallback for a photo-less item.
	ex(`INSERT INTO items (id, sku, name, base_price, is_active, is_weighed, unit) VALUES ('item-b','SKU-B','Cappuccino',320,1,0,'each')`)
	ex(`INSERT INTO item_images (id, item_id, path, role) VALUES ('img-b','item-b','/public/assets/items/item-b/thumb.png','thumbnail')`)
	ex(`INSERT INTO shortcut_buttons (barcode, item_id, label, image_path, sort_order) VALUES ('BTN-B','item-b','Cappuccino','/public/images/custom-cappuccino.png',2)`)

	// item-d: explicit button image, no catalog photo — the button's own
	// image_path is NOT a picture source (ut-docs#6076): it can be a stale
	// frozen copy that neither clear path knows about.
	ex(`INSERT INTO items (id, sku, name, base_price, is_active, is_weighed, unit) VALUES ('item-d','SKU-D','Muffin',250,1,0,'each')`)
	ex(`INSERT INTO shortcut_buttons (barcode, item_id, label, image_path, sort_order) VALUES ('BTN-D','item-d','Muffin','/public/images/custom-muffin.png',4)`)

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
	if got := byLabel["Cappuccino"].ImageURL; got != "/public/assets/items/item-b/thumb.png" {
		t.Fatalf("expected the catalog photo to win over the button image for Cappuccino (ut-docs#4838), got %q", got)
	}
	if got := byLabel["Muffin"].ImageURL; got != "" {
		t.Fatalf("expected no image for photo-less Muffin (button image_path is not a picture source, ut-docs#6076), got %q", got)
	}
	if got := byLabel["Croissant"].ImageURL; got != "" {
		t.Fatalf("expected no image for Croissant, got %q", got)
	}
}

// TestLoadButtons_CarriesItemCategoryID pins that each loaded button carries
// its item's category_id — the sale-screen grid needs this to group tiles
// under their category. An item with no category must come back with an
// empty CategoryID, not error or a synthetic value.
func TestLoadButtons_CarriesItemCategoryID(t *testing.T) {
	d, err := db.Open(testsupport.MigratedDBFile(t, "shortcuts.db"))
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

	ex(`INSERT INTO categories (id, name) VALUES ('cat-drinks','Drinks')`)
	ex(`INSERT INTO items (id, sku, name, base_price, is_active, is_weighed, unit, category_id) VALUES ('item-a','SKU-A','Latte',320,1,0,'each','cat-drinks')`)
	ex(`INSERT INTO shortcut_buttons (barcode, item_id, label, sort_order) VALUES ('BTN-A','item-a','Latte',1)`)

	ex(`INSERT INTO items (id, sku, name, base_price, is_active, is_weighed, unit) VALUES ('item-b','SKU-B','Loose Sweet',10,1,0,'each')`)
	ex(`INSERT INTO shortcut_buttons (barcode, item_id, label, sort_order) VALUES ('BTN-B','item-b','Loose Sweet',2)`)

	repo := data.NewShortcutsRepo(d.DB)
	btns, err := repo.LoadButtons(ctx)
	if err != nil {
		t.Fatalf("LoadButtons: %v", err)
	}
	byLabel := map[string]data.ShortcutButton{}
	for _, b := range btns {
		byLabel[b.Label] = b
	}
	if got := byLabel["Latte"].CategoryID; got != "cat-drinks" {
		t.Fatalf("expected CategoryID cat-drinks for Latte, got %q", got)
	}
	if got := byLabel["Loose Sweet"].CategoryID; got != "" {
		t.Fatalf("expected empty CategoryID for an uncategorized item, got %q", got)
	}
}

// TestLoadButtons_CarriesItemColor pins that each loaded button carries its
// item's color (ut-docs#1901), same shape as CategoryID above — the
// sale-screen grid needs this to paint a photo-less item's tile. An item
// with no color must come back with an empty Color, not error or a
// synthetic value. Uses the real migrated schema (db.Open), unlike the
// ui/data packages' hand-rolled test schemas, so this also guards that
// migration 017 actually adds items.color.
func TestLoadButtons_CarriesItemColor(t *testing.T) {
	d, err := db.Open(testsupport.MigratedDBFile(t, "shortcuts.db"))
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

	ex(`INSERT INTO items (id, sku, name, base_price, is_active, is_weighed, unit, color) VALUES ('item-a','SKU-A','Latte',320,1,0,'each','#0f172a')`)
	ex(`INSERT INTO shortcut_buttons (barcode, item_id, label, sort_order) VALUES ('BTN-A','item-a','Latte',1)`)

	ex(`INSERT INTO items (id, sku, name, base_price, is_active, is_weighed, unit) VALUES ('item-b','SKU-B','Loose Sweet',10,1,0,'each')`)
	ex(`INSERT INTO shortcut_buttons (barcode, item_id, label, sort_order) VALUES ('BTN-B','item-b','Loose Sweet',2)`)

	repo := data.NewShortcutsRepo(d.DB)
	btns, err := repo.LoadButtons(ctx)
	if err != nil {
		t.Fatalf("LoadButtons: %v", err)
	}
	byLabel := map[string]data.ShortcutButton{}
	for _, b := range btns {
		byLabel[b.Label] = b
	}
	if got := byLabel["Latte"].Color; got != "#0f172a" {
		t.Fatalf("expected Color #0f172a for Latte, got %q", got)
	}
	if got := byLabel["Loose Sweet"].Color; got != "" {
		t.Fatalf("expected empty Color for an item with no color set, got %q", got)
	}
}

// TestLoadButtons_ExcludesInactiveOrMissingItems pins ut-docs#2281 cause A:
// Catalog "Delete item" is a soft delete (CatalogRepo.DeactivateItem ->
// is_active=0), but LoadButtons used to return a button for it regardless,
// so the sell-screen tile stayed put — tapping it silently did nothing,
// since POSRepo.ResolveShortcutLineDecoded already filters i.is_active = 1
// when actually resolving a tap. A button whose item row is gone entirely
// must also be dropped, not surfaced with zeroed-out fields (ON DELETE
// CASCADE ordinarily removes it with the item; this forces the dangling
// row directly to pin the join's own defensive behavior too).
func TestLoadButtons_ExcludesInactiveOrMissingItems(t *testing.T) {
	d, err := db.Open(testsupport.MigratedDBFile(t, "shortcuts.db"))
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

	ex(`INSERT INTO items (id, sku, name, base_price, is_active, is_weighed, unit) VALUES ('item-a','SKU-A','Latte',320,1,0,'each')`)
	ex(`INSERT INTO shortcut_buttons (barcode, item_id, label, sort_order) VALUES ('BTN-A','item-a','Latte',1)`)

	ex(`INSERT INTO items (id, sku, name, base_price, is_active, is_weighed, unit) VALUES ('item-b','SKU-B','Discontinued Cake',280,0,0,'each')`)
	ex(`INSERT INTO shortcut_buttons (barcode, item_id, label, sort_order) VALUES ('BTN-B','item-b','Discontinued Cake',2)`)

	ex(`PRAGMA foreign_keys = OFF`)
	ex(`INSERT INTO items (id, sku, name, base_price, is_active, is_weighed, unit) VALUES ('item-c','SKU-C','Ghost',150,1,0,'each')`)
	ex(`INSERT INTO shortcut_buttons (barcode, item_id, label, sort_order) VALUES ('BTN-C','item-c','Ghost',3)`)
	ex(`DELETE FROM items WHERE id = 'item-c'`)
	ex(`PRAGMA foreign_keys = ON`)

	repo := data.NewShortcutsRepo(d.DB)
	btns, err := repo.LoadButtons(ctx)
	if err != nil {
		t.Fatalf("LoadButtons: %v", err)
	}
	byLabel := map[string]data.ShortcutButton{}
	for _, b := range btns {
		byLabel[b.Label] = b
	}
	if _, ok := byLabel["Latte"]; !ok {
		t.Fatalf("expected the active item's button to load, got %+v", btns)
	}
	if _, ok := byLabel["Discontinued Cake"]; ok {
		t.Fatalf("expected the deactivated item's button excluded, got %+v", btns)
	}
	if _, ok := byLabel["Ghost"]; ok {
		t.Fatalf("expected a button whose item row is gone excluded, got %+v", btns)
	}
	if len(btns) != 1 {
		t.Fatalf("expected exactly 1 button, got %d: %+v", len(btns), btns)
	}
}

// ut-docs#6076: shortcut_buttons.image_path is never a picture source. The
// demo seed froze each item's bundled thumb there, and neither clear path
// (ClearItemPhoto / ClearItemThumbnail) touches it, so a cleared item kept
// showing the stock photo on its quick tile.
func TestLoadButtons_ImageOnlyFromItemThumbnail_6076(t *testing.T) {
	d, err := db.Open(testsupport.MigratedDBFile(t, "shortcuts6076.db"))
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
	// item-x: frozen bundled thumb in image_path, item_images row cleared.
	ex(`INSERT INTO items (id, sku, name, base_price, is_active, is_weighed, unit) VALUES ('item-x','SKU-X','Xtile',100,1,0,'each')`)
	ex(`INSERT INTO shortcut_buttons (barcode, item_id, label, image_path, sort_order) VALUES ('BTN-X','item-x','Xtile','/public/assets/items/item-x/thumb.png',1)`)
	// item-y: frozen library tile in image_path, no photo.
	ex(`INSERT INTO items (id, sku, name, base_price, is_active, is_weighed, unit) VALUES ('item-y','SKU-Y','Ytile',100,1,0,'each')`)
	ex(`INSERT INTO shortcut_buttons (barcode, item_id, label, image_path, sort_order) VALUES ('BTN-Y','item-y','Ytile',?,2)`, iconid.AssetPath("lucide:beer"))

	repo := data.NewShortcutsRepo(d.DB)
	for name, load := range map[string]func(context.Context) ([]data.ShortcutButton, error){
		"LoadButtons":     repo.LoadButtons,
		"LoadGridButtons": repo.LoadGridButtons,
	} {
		btns, err := load(ctx)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(btns) != 2 {
			t.Fatalf("%s: got %d buttons, want 2", name, len(btns))
		}
		for _, b := range btns {
			if b.ImageURL != "" {
				t.Errorf("%s: %s ImageURL = %q, want empty (image_path is not a picture source)", name, b.Label, b.ImageURL)
			}
		}
	}
}
