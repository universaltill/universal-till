package data_test

import (
	"context"
	"errors"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// ut-docs#2717: one picture per category, last writer wins. A
// save_category directive (my.) that sets an icon clears image_path in the
// same transaction — a library tile the till picked and an uploaded photo
// alike — and reports what it cleared so the caller can delete a
// superseded upload's file. An empty icon ("cleared") leaves an image
// alone.
func TestSaveCategory_IconClearsImagePath(t *testing.T) {
	f := newSaveFixture(t)
	ctx := context.Background()
	const tile = "/public/assets/category-icons/beer.svg"
	f.exec(t, `UPDATE categories SET image_path = ? WHERE id = 'cat1'`, tile)

	res, err := f.catalog.SaveCategory(ctx, data.CategorySave{ID: "cat1", Icon: strp("lucide:coffee")})
	if err != nil {
		t.Fatal(err)
	}
	if res.ClearedImagePath != tile {
		t.Fatalf("ClearedImagePath = %q, want %q", res.ClearedImagePath, tile)
	}
	if got := f.str(t, `SELECT COALESCE(image_path,'-')||'|'||COALESCE(icon,'-') FROM categories WHERE id = 'cat1'`); got != "-|lucide:coffee" {
		t.Fatalf("after icon directive: %q, want the image cleared and the icon set", got)
	}

	// An uploaded photo is replaced the same way (explicit owner intent).
	const photo = "/public/assets/categories/cat1/thumb.png"
	f.exec(t, `UPDATE categories SET image_path = ?, icon = NULL WHERE id = 'cat1'`, photo)
	res, err = f.catalog.SaveCategory(ctx, data.CategorySave{ID: "cat1", Icon: strp("lucide:soup")})
	if err != nil || res.ClearedImagePath != photo {
		t.Fatalf("icon over photo: res=%+v err=%v", res, err)
	}
	if got := f.str(t, `SELECT COALESCE(image_path,'-')||'|'||COALESCE(icon,'-') FROM categories WHERE id = 'cat1'`); got != "-|lucide:soup" {
		t.Fatalf("after icon over photo: %q", got)
	}

	// icon "" (cleared on my.) leaves an image alone, and so does a save
	// that doesn't touch the icon at all.
	f.exec(t, `UPDATE categories SET image_path = ?, icon = NULL WHERE id = 'cat1'`, photo)
	for _, p := range []data.CategorySave{{ID: "cat1", Icon: strp("")}, {ID: "cat1", Name: strp("Drinks")}} {
		res, err := f.catalog.SaveCategory(ctx, p)
		if err != nil || res.ClearedImagePath != "" {
			t.Fatalf("%+v: res=%+v err=%v", p, res, err)
		}
		if got := f.str(t, `SELECT COALESCE(image_path,'-') FROM categories WHERE id = 'cat1'`); got != photo {
			t.Fatalf("%+v: image_path = %q, want the photo kept", p, got)
		}
	}
}

// The till's own editor writes both columns at once (ut-docs#2717): a
// library pick stores the icon id with no path, an upload stores the path
// with no icon, "No image" clears both.
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

// #2717 review: a pre-#2717 row keeps a library tile in image_path with no
// icon; the till draws (and the snapshot reports) it as that tile's icon id.
// my. clearing the icon (icon "") must then clear the tile too, or my. shows
// "no icon" while the till keeps drawing it. An uploaded photo stays.
func TestSaveCategory_ClearedIconAlsoClearsLegacyLibraryTile(t *testing.T) {
	f := newSaveFixture(t)
	ctx := context.Background()
	const tile = "/public/assets/category-icons/beer.svg"
	f.exec(t, `UPDATE categories SET image_path = ?, icon = NULL WHERE id = 'cat1'`, tile)
	res, err := f.catalog.SaveCategory(ctx, data.CategorySave{ID: "cat1", Icon: strp("")})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.str(t, `SELECT COALESCE(image_path,'-')||'|'||COALESCE(icon,'-') FROM categories WHERE id = 'cat1'`); got != "-|-" {
		t.Fatalf("cleared icon on a legacy library tile: got %q, want both cleared", got)
	}
	if res.ClearedImagePath != "" {
		t.Fatalf("a library tile is not an upload; ClearedImagePath = %q, want empty", res.ClearedImagePath)
	}
}
