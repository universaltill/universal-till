package data_test

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/iconid"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// ut-docs#5124: an icon set on my. lands in items.icon with NO item_images
// row. Every resolve path that builds a basket line must draw the same one
// picture the sell-screen tile does, not an empty box.
func TestResolve_IconOnlyItemHasBasketPicture(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	seedSettingsTable(t, db)
	repo := data.NewPOSRepo(db)
	ctx := context.Background()
	if err := data.NewSettingsRepo(db).SetEnabledBarcodeSymbologies(ctx, []string{"EAN13"}); err != nil {
		t.Fatal(err)
	}
	ean := ean13t(t, "400638133393")
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "KW1", Name: "Icon Item", BasePrice: 300, IsActive: true})
	if _, err := db.Exec(`UPDATE items SET icon = 'lucide:beer' WHERE id = 'i1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO item_barcodes(barcode, item_id, is_primary) VALUES('BC1','i1',1), ('` + ean + `','i1',0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO shortcut_buttons(barcode, item_id, label) VALUES('BTN1','i1','Icon Item')`); err != nil {
		t.Fatal(err)
	}
	want := iconid.AssetPath("lucide:beer")
	if want == "" {
		t.Fatal("test icon not in registry")
	}
	for _, code := range []string{ean, "BTN1", "KW1", "item:i1", "Icon Ite"} {
		line, ok := repo.ResolveShortcutLine(ctx, code)
		if !ok {
			t.Fatalf("%s: not resolved", code)
		}
		if line.ImageURL != want {
			t.Errorf("%s: ImageURL = %q, want the icon's artwork %q", code, line.ImageURL, want)
		}
	}
	line, _, ok := repo.ResolveScanLine(ctx, ean, []string{"EAN13"})
	if !ok || line.ImageURL != want {
		t.Errorf("scan: ok=%v ImageURL=%q, want %q", ok, line.ImageURL, want)
	}
}

// A photo still wins over the icon, and an item with neither has no picture.
func TestResolve_PhotoBeatsIconAndNoneIsEmpty(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	repo := data.NewPOSRepo(db)
	ctx := context.Background()
	seedSettingsTable(t, db)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "p1", SKU: "PH", Name: "Photo", BasePrice: 100, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "n1", SKU: "NO", Name: "Nothing", BasePrice: 100, IsActive: true})
	if _, err := db.Exec(`UPDATE items SET icon = 'lucide:beer' WHERE id = 'p1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO item_images(item_id, path, role, sort_order) VALUES('p1','/public/assets/items/p1/thumb.png','thumbnail',0)`); err != nil {
		t.Fatal(err)
	}
	if l, ok := repo.ResolveShortcutLine(ctx, "PH"); !ok || l.ImageURL != "/public/assets/items/p1/thumb.png" {
		t.Errorf("photo: ok=%v ImageURL=%q", ok, l.ImageURL)
	}
	if l, ok := repo.ResolveShortcutLine(ctx, "NO"); !ok || l.ImageURL != "" {
		t.Errorf("none: ok=%v ImageURL=%q", ok, l.ImageURL)
	}
}

// The variant resolve paths (variant SKU, variant name, variant barcode)
// carry the parent item's icon too.
func TestResolve_VariantOfIconOnlyItemHasBasketPicture(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	seedSettingsTable(t, db)
	repo := data.NewPOSRepo(db)
	ctx := context.Background()
	if err := data.NewSettingsRepo(db).SetEnabledBarcodeSymbologies(ctx, []string{"EAN13"}); err != nil {
		t.Fatal(err)
	}
	ean := ean13t(t, "400638133394")
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "LATTE", Name: "Latte", BasePrice: 300, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v1", ItemID: "i1", SKU: "LATTE-L", Name: "Largexyz", Price: 350, IsActive: true})
	if _, err := db.Exec(`UPDATE items SET icon = 'lucide:beer' WHERE id = 'i1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO variant_barcodes(barcode, variant_id, is_primary) VALUES(?, 'v1', 1)`, ean); err != nil {
		t.Fatal(err)
	}
	want := iconid.AssetPath("lucide:beer")
	for _, code := range []string{"LATTE-L", "Largexyz", ean} {
		line, ok := repo.ResolveShortcutLine(ctx, code)
		if !ok || line.VariantID != "v1" || line.ImageURL != want {
			t.Errorf("%s: ok=%v variant=%q ImageURL=%q, want v1 / %q", code, ok, line.VariantID, line.ImageURL, want)
		}
	}
	if line, _, ok := repo.ResolveScanLine(ctx, ean, []string{"EAN13"}); !ok || line.VariantID != "v1" || line.ImageURL != want {
		t.Errorf("scan: ok=%v variant=%q ImageURL=%q", ok, line.VariantID, line.ImageURL)
	}
}
