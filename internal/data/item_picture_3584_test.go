package data_test

import (
	"context"
	"errors"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#3584 gave an item the one-picture rule a category had; ut-docs#3601
// relaxes it the way ut-docs#3585 did for categories: a thumbnail path in
// item_images and an icon id in items.icon (migration 066) may both be
// stored, the photo wins on display (iconid.Resolve), and each write sets
// exactly the pair it is given.

const (
	itemPhoto3584 = "/public/assets/items/itm1/thumb.png"
	itemTile3584  = "/public/assets/category-icons/beer.svg"
)

// picture reads itm1's thumbnail path and icon as "path|icon", "-" for none.
func (f saveFixture) picture(t *testing.T) string {
	t.Helper()
	return f.str(t, `SELECT COALESCE((SELECT path FROM item_images WHERE item_id = 'itm1' AND role = 'thumbnail'), '-')
	                     || '|' || COALESCE((SELECT icon FROM items WHERE id = 'itm1'), '-')`)
}

func TestSetItemPicture_LastWriterPerColumn(t *testing.T) {
	f := newSaveFixture(t)
	ctx := context.Background()

	if err := f.catalog.SetItemPicture(ctx, "itm1", itemPhoto3584, ""); err != nil {
		t.Fatal(err)
	}
	if got := f.picture(t); got != itemPhoto3584+"|-" {
		t.Fatalf("photo: %q", got)
	}
	if err := f.catalog.SetItemPicture(ctx, "itm1", "", "lucide:beer"); err != nil {
		t.Fatal(err)
	}
	if got := f.picture(t); got != "-|lucide:beer" {
		t.Fatalf("icon over photo: %q, want the thumbnail row gone", got)
	}
	if err := f.catalog.SetItemPicture(ctx, "itm1", itemPhoto3584, ""); err != nil {
		t.Fatal(err)
	}
	if got := f.picture(t); got != itemPhoto3584+"|-" {
		t.Fatalf("photo over icon: %q, want the icon cleared", got)
	}
	if err := f.catalog.SetItemPicture(ctx, "itm1", "", ""); err != nil {
		t.Fatal(err)
	}
	if got := f.picture(t); got != "-|-" {
		t.Fatalf("none: %q, want both cleared (NULL icon)", got)
	}

	if err := f.catalog.SetItemPicture(ctx, "itm1", "", "not an id"); err == nil {
		t.Fatal("a malformed icon id must be refused")
	}
	if err := f.catalog.SetItemPicture(ctx, "nope", "", "lucide:beer"); !errors.Is(err, data.ErrItemNotFound) {
		t.Fatalf("unknown item: %v, want ErrItemNotFound", err)
	}
	if got := f.picture(t); got != "-|-" {
		t.Fatalf("refused writes changed the item: %q", got)
	}
	// ut-docs#3601: a path and an icon together are now stored (photo wins).
	if err := f.catalog.SetItemPicture(ctx, "itm1", itemPhoto3584, "lucide:beer"); err != nil {
		t.Fatal(err)
	}
	if got := f.picture(t); got != itemPhoto3584+"|lucide:beer" {
		t.Fatalf("both: %q", got)
	}
}

// The till picker's library pick (SetItemThumbnail) and "No image"
// (ClearItemThumbnail) replace / clear both columns — unchanged by
// ut-docs#3601; uploaded photos go through SetItemPhoto, which keeps the icon.
func TestItemThumbnailWrites_ClearTheIcon(t *testing.T) {
	f := newSaveFixture(t)
	ctx := context.Background()

	f.exec(t, `UPDATE items SET icon = 'lucide:beer' WHERE id = 'itm1'`)
	if err := f.catalog.SetItemThumbnail(ctx, "itm1", itemTile3584); err != nil {
		t.Fatal(err)
	}
	if got := f.picture(t); got != itemTile3584+"|-" {
		t.Fatalf("till pick over icon: %q, want the icon cleared", got)
	}

	f.exec(t, `DELETE FROM item_images WHERE item_id = 'itm1'`)
	f.exec(t, `UPDATE items SET icon = 'lucide:beer' WHERE id = 'itm1'`)
	if err := f.catalog.ClearItemThumbnail(ctx, "itm1"); err != nil {
		t.Fatal(err)
	}
	if got := f.picture(t); got != "-|-" {
		t.Fatalf("till No image over icon: %q, want the icon cleared too", got)
	}

	// The importer's placeholder never lands on an item that already shows
	// an icon (it fills only an item with no picture at all).
	f.exec(t, `UPDATE items SET icon = 'lucide:beer' WHERE id = 'itm1'`)
	if err := f.catalog.EnsureDefaultThumbnail(ctx, "itm1", itemTile3584); err != nil {
		t.Fatal(err)
	}
	if got := f.picture(t); got != "-|lucide:beer" {
		t.Fatalf("placeholder over icon: %q, want the icon alone", got)
	}
}

// save_item's icon keeps an uploaded photo (ut-docs#6028, as SaveCategory's
// #3585): the photo shows, the icon is its fallback.
func TestSaveItem_Icon(t *testing.T) {
	f := newSaveFixture(t)
	ctx := context.Background()

	// A malformed id is refused, before anything is written.
	if _, err := f.catalog.SaveItem(ctx, data.ItemPatch{ID: "itm1", Icon: strp("<svg>"), Name: strp("Changed")}); err == nil {
		t.Fatal("a malformed icon id must be refused")
	}
	if got := f.str(t, `SELECT name FROM items WHERE id = 'itm1'`); got != "Flat White" {
		t.Fatalf("refused patch wrote the name: %q", got)
	}

	// An icon keeps an uploaded photo (ut-docs#6028): nothing is reported
	// as cleared, so no file is removed.
	if err := f.catalog.SetItemPhoto(ctx, "itm1", itemPhoto3584); err != nil {
		t.Fatal(err)
	}
	res, err := f.catalog.SaveItem(ctx, data.ItemPatch{ID: "itm1", Icon: strp("  lucide:coffee ")})
	if err != nil {
		t.Fatal(err)
	}
	if res.ClearedImagePath != "" {
		t.Fatalf("ClearedImagePath = %q, want none (the photo is kept)", res.ClearedImagePath)
	}
	if !contains(res.Changed, "icon") {
		t.Fatalf("Changed = %v, want icon listed", res.Changed)
	}
	if got := f.picture(t); got != itemPhoto3584+"|lucide:coffee" {
		t.Fatalf("after icon: %q", got)
	}

	// Re-applying the same patch (a lost-result replay) changes nothing.
	if res, err = f.catalog.SaveItem(ctx, data.ItemPatch{ID: "itm1", Icon: strp("lucide:coffee")}); err != nil || res.ClearedImagePath != "" {
		t.Fatalf("replay: res=%+v err=%v", res, err)
	}
	if got := f.picture(t); got != itemPhoto3584+"|lucide:coffee" {
		t.Fatalf("after replay: %q", got)
	}

	// A save that leaves the icon out keeps it.
	if _, err := f.catalog.SaveItem(ctx, data.ItemPatch{ID: "itm1", Name: strp("Latte")}); err != nil {
		t.Fatal(err)
	}
	if got := f.picture(t); got != itemPhoto3584+"|lucide:coffee" {
		t.Fatalf("icon-less save: %q, want photo and icon kept", got)
	}

	// icon "" clears the icon and leaves an uploaded photo alone…
	if res, err = f.catalog.SaveItem(ctx, data.ItemPatch{ID: "itm1", Icon: strp("")}); err != nil || res.ClearedImagePath != "" {
		t.Fatalf("clear over photo: res=%+v err=%v", res, err)
	}
	if got := f.picture(t); got != itemPhoto3584+"|-" {
		t.Fatalf("clear over photo: %q, want the photo kept", got)
	}

	// …but a library tile the till picked IS the item's icon (it is drawn
	// and reported as that id), so clearing the icon clears the tile too.
	if err := f.catalog.SetItemThumbnail(ctx, "itm1", itemTile3584); err != nil {
		t.Fatal(err)
	}
	if _, err = f.catalog.SaveItem(ctx, data.ItemPatch{ID: "itm1", Icon: strp("")}); err != nil {
		t.Fatal(err)
	}
	if got := f.picture(t); got != "-|-" {
		t.Fatalf("clear over library tile: %q, want the tile cleared", got)
	}
}

// A brand-new item can carry its icon in the create directive (my. lets the
// owner pick an icon before the first save).
func TestSaveItem_CreateWithIcon(t *testing.T) {
	f := newSaveFixture(t)
	ctx := context.Background()
	res, err := f.catalog.SaveItem(ctx, data.ItemPatch{ID: "itm-new", Create: true, Name: strp("Lager"), PriceMinor: i64p(450), Icon: strp("lucide:beer")})
	if err != nil || !res.Created {
		t.Fatalf("create: res=%+v err=%v", res, err)
	}
	if got := f.str(t, `SELECT COALESCE(icon, '-') FROM items WHERE id = 'itm-new'`); got != "lucide:beer" {
		t.Fatalf("icon = %q", got)
	}
}

func TestItemIconsAndSnapshot(t *testing.T) {
	f := newSaveFixture(t)
	ctx := context.Background()
	if err := f.catalog.SetItemPicture(ctx, "itm1", "", "lucide:beer"); err != nil {
		t.Fatal(err)
	}
	icons, err := f.catalog.ItemIcons(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if icons["itm1"] != "lucide:beer" || len(icons) != 1 {
		t.Fatalf("ItemIcons = %v, want only itm1 → lucide:beer", icons)
	}
	items, err := f.catalog.CatalogSnapshotItems(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		want := ""
		if it.ID == "itm1" {
			want = "lucide:beer"
		}
		if it.Icon != want {
			t.Fatalf("snapshot item %s Icon = %q, want %q", it.ID, it.Icon, want)
		}
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
