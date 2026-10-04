package data_test

import (
	"context"
	"errors"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// ut-docs#2717, #3585: a save_category directive (my.) that sets an icon
// replaces a legacy library tile in image_path (the tile IS an icon, and
// iconid.Resolve already draws the newer icon over it), but keeps an
// uploaded photo: a category may have both, the photo shown, the icon its
// fallback. An empty icon ("cleared") leaves a photo alone too.
func TestSaveCategory_IconReplacesLibraryTileButKeepsPhoto(t *testing.T) {
	f := newSaveFixture(t)
	ctx := context.Background()
	const tile = "/public/assets/category-icons/beer.svg"
	f.exec(t, `UPDATE categories SET image_path = ? WHERE id = 'cat1'`, tile)

	if _, err := f.catalog.SaveCategory(ctx, data.CategorySave{ID: "cat1", Icon: strp("lucide:coffee")}); err != nil {
		t.Fatal(err)
	}
	if got := f.str(t, `SELECT COALESCE(image_path,'-')||'|'||COALESCE(icon,'-') FROM categories WHERE id = 'cat1'`); got != "-|lucide:coffee" {
		t.Fatalf("icon over a library tile: %q, want the tile cleared and the icon set", got)
	}
	// The id-less generic tile is a library tile too.
	const generic = "/public/assets/category-icons/generic.svg"
	f.exec(t, `UPDATE categories SET image_path = ?, icon = NULL WHERE id = 'cat1'`, generic)
	if _, err := f.catalog.SaveCategory(ctx, data.CategorySave{ID: "cat1", Icon: strp("lucide:leaf")}); err != nil {
		t.Fatal(err)
	}
	if got := f.str(t, `SELECT COALESCE(image_path,'-')||'|'||COALESCE(icon,'-') FROM categories WHERE id = 'cat1'`); got != "-|lucide:leaf" {
		t.Fatalf("icon over the generic tile: %q, want the tile cleared and the icon set", got)
	}

	// ut-docs#3585: an uploaded photo is kept; the icon is stored under it.
	const photo = "/public/assets/categories/cat1/thumb.png"
	f.exec(t, `UPDATE categories SET image_path = ?, icon = 'lucide:coffee' WHERE id = 'cat1'`, photo)
	if _, err := f.catalog.SaveCategory(ctx, data.CategorySave{ID: "cat1", Icon: strp("lucide:soup")}); err != nil {
		t.Fatal(err)
	}
	if got := f.str(t, `SELECT COALESCE(image_path,'-')||'|'||COALESCE(icon,'-') FROM categories WHERE id = 'cat1'`); got != photo+"|lucide:soup" {
		t.Fatalf("icon under a photo: %q, want the photo kept and the icon changed", got)
	}

	// icon "" (cleared on my.) leaves a photo alone, and so does a save
	// that doesn't touch the icon at all.
	for _, p := range []data.CategorySave{{ID: "cat1", Icon: strp("")}, {ID: "cat1", Name: strp("Drinks")}} {
		if _, err := f.catalog.SaveCategory(ctx, p); err != nil {
			t.Fatalf("%+v: %v", p, err)
		}
		if got := f.str(t, `SELECT COALESCE(image_path,'-') FROM categories WHERE id = 'cat1'`); got != photo {
			t.Fatalf("%+v: image_path = %q, want the photo kept", p, got)
		}
	}
}

// SetCategoryPicture writes both columns at once, exactly as given
// (ut-docs#2717): an empty argument stores NULL in that column. Which
// column a caller preserves is the caller's choice (ut-docs#3585: an upload
// or "No image" from the dialog keeps the stored icon; a library pick still
// clears the path).
func TestSetCategoryPicture_WritesBothColumns(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()
	id, err := repo.CreateCategory(ctx, "Drinks")
	if err != nil {
		t.Fatal(err)
	}
	read := func() (string, string) {
		t.Helper()
		all, err := repo.ListCategories(ctx)
		if err != nil || len(all) != 1 {
			t.Fatalf("ListCategories: %+v %v", all, err)
		}
		return all[0].ImagePath, all[0].Icon
	}
	const photo = "/public/assets/categories/x/thumb.png"
	if err := repo.SetCategoryPicture(ctx, id, photo, ""); err != nil {
		t.Fatal(err)
	}
	if p, ic := read(); p != photo || ic != "" {
		t.Fatalf("photo: (%q, %q)", p, ic)
	}
	if err := repo.SetCategoryPicture(ctx, id, "", "lucide:beer"); err != nil {
		t.Fatal(err)
	}
	if p, ic := read(); p != "" || ic != "lucide:beer" {
		t.Fatalf("icon over photo: (%q, %q), want the path cleared", p, ic)
	}
	if err := repo.SetCategoryPicture(ctx, id, photo, ""); err != nil {
		t.Fatal(err)
	}
	if p, ic := read(); p != photo || ic != "" {
		t.Fatalf("photo over icon: (%q, %q), want the icon cleared", p, ic)
	}
	if err := repo.SetCategoryPicture(ctx, id, "", ""); err != nil {
		t.Fatal(err)
	}
	var nulls int
	if err := db.QueryRow(`SELECT (image_path IS NULL) + (icon IS NULL) FROM categories WHERE id = ?`, id).Scan(&nulls); err != nil || nulls != 2 {
		t.Fatalf("none must store NULL in both: nulls=%d err=%v", nulls, err)
	}
	if err := repo.SetCategoryPicture(ctx, id, "", "not an id"); err == nil {
		t.Fatal("a malformed icon id must be refused")
	}
	if err := repo.SetCategoryPicture(ctx, "nope", "", "lucide:beer"); !errors.Is(err, data.ErrCategoryNotFound) {
		t.Fatalf("unknown id: %v", err)
	}
}

// ut-docs#3585: the two columns are independent — a category may keep an
// uploaded photo AND an icon id at once (the sale screen shows the image,
// else the icon: iconid.Resolve). SetCategoryPicture no longer refuses the
// pair, and both values round-trip unchanged.
func TestSetCategoryPicture_ImageAndIconCoexist(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()
	id, err := repo.CreateCategory(ctx, "Drinks")
	if err != nil {
		t.Fatal(err)
	}
	photo := "/public/assets/categories/" + id + "/thumb.png"
	if err := repo.SetCategoryPicture(ctx, id, photo, "lucide:beer"); err != nil {
		t.Fatalf("image + icon must both be storable now: %v", err)
	}
	got, ok, err := repo.CategoryPicture(ctx, id)
	if err != nil || !ok {
		t.Fatalf("CategoryPicture: ok=%v err=%v", ok, err)
	}
	if got.ImagePath != photo || got.Icon != "lucide:beer" {
		t.Fatalf("round trip = (%q, %q), want (%q, %q)", got.ImagePath, got.Icon, photo, "lucide:beer")
	}
	// The icon format is still validated when an image rides along.
	if err := repo.SetCategoryPicture(ctx, id, photo, "not an id"); err == nil {
		t.Fatal("a malformed icon id must still be refused alongside an image")
	}
}

// #2717 review: a pre-#2717 row keeps a library tile in image_path with no
// icon; the till draws (and the snapshot reports) it as that tile's icon id.
// my. clearing the icon (icon "") must then clear the tile too, or my. shows
// "no icon" while the till keeps drawing it. An uploaded photo stays.
func TestSaveCategory_ClearedIconAlsoClearsLegacyLibraryTile(t *testing.T) {
	f := newSaveFixture(t)
	ctx := context.Background()
	const tile = "/public/assets/category-icons/beer.svg"
	f.exec(t, `UPDATE categories SET image_path = ?, icon = NULL WHERE id = 'cat1'`, tile)
	if _, err := f.catalog.SaveCategory(ctx, data.CategorySave{ID: "cat1", Icon: strp("")}); err != nil {
		t.Fatal(err)
	}
	if got := f.str(t, `SELECT COALESCE(image_path,'-')||'|'||COALESCE(icon,'-') FROM categories WHERE id = 'cat1'`); got != "-|-" {
		t.Fatalf("cleared icon on a legacy library tile: got %q, want both cleared", got)
	}
}
