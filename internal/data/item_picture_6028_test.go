package data_test

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#6028: save_item's icon keeps an uploaded photo (the item
// counterpart of SaveCategory's #3585 rule); a library tile is still
// replaced by the icon.

func TestSaveItemIcon_KeepsUploadedPhoto(t *testing.T) {
	f := newSaveFixture(t)
	ctx := context.Background()
	if err := f.catalog.SetItemPhoto(ctx, "itm1", itemPhoto3584); err != nil {
		t.Fatal(err)
	}
	res, err := f.catalog.SaveItem(ctx, data.ItemPatch{ID: "itm1", Icon: strp("lucide:beer")})
	if err != nil {
		t.Fatal(err)
	}
	if res.ClearedImagePath != "" {
		t.Fatalf("ClearedImagePath = %q, want none", res.ClearedImagePath)
	}
	if got := f.picture(t); got != itemPhoto3584+"|lucide:beer" {
		t.Fatalf("photo + icon: %q", got)
	}
	// Removing the photo leaves the icon as the fallback.
	if err := f.catalog.ClearItemPhoto(ctx, "itm1"); err != nil {
		t.Fatal(err)
	}
	if got := f.picture(t); got != "-|lucide:beer" {
		t.Fatalf("after photo removed: %q, want the icon alone", got)
	}
}

func TestSaveItemIcon_ReplacesLibraryTile(t *testing.T) {
	for _, tile := range []string{itemTile3584, "/public/assets/category-icons/generic.svg"} {
		f := newSaveFixture(t)
		ctx := context.Background()
		if err := f.catalog.SetItemThumbnail(ctx, "itm1", tile); err != nil {
			t.Fatal(err)
		}
		res, err := f.catalog.SaveItem(ctx, data.ItemPatch{ID: "itm1", Icon: strp("lucide:beer")})
		if err != nil {
			t.Fatal(err)
		}
		if res.ClearedImagePath != tile {
			t.Fatalf("%s: ClearedImagePath = %q, want the tile", tile, res.ClearedImagePath)
		}
		if got := f.picture(t); got != "-|lucide:beer" {
			t.Fatalf("%s: %q, want the icon alone", tile, got)
		}
	}
}

func TestSaveItemIcon_ChangeIconUnderPhoto(t *testing.T) {
	f := newSaveFixture(t)
	ctx := context.Background()
	if err := f.catalog.SetItemPhoto(ctx, "itm1", itemPhoto3584); err != nil {
		t.Fatal(err)
	}
	for _, icon := range []string{"lucide:beer", "lucide:coffee"} {
		if _, err := f.catalog.SaveItem(ctx, data.ItemPatch{ID: "itm1", Icon: strp(icon)}); err != nil {
			t.Fatal(err)
		}
		if got := f.picture(t); got != itemPhoto3584+"|"+icon {
			t.Fatalf("icon %s: %q", icon, got)
		}
	}
}

// A3 regressions (Tester, ut-docs#6028): save_item icon "" on a photo keeps
// the photo and NULLs the icon; on a named library tile (with or
// without a stored icon) it clears both; the generic placeholder stays.
func TestSaveItemIcon_ClearRegressions(t *testing.T) {
	t.Run("photo", func(t *testing.T) {
		f := newSaveFixture(t)
		ctx := context.Background()
		if err := f.catalog.SetItemPicture(ctx, "itm1", itemPhoto3584, "lucide:beer"); err != nil {
			t.Fatal(err)
		}
		res, err := f.catalog.SaveItem(ctx, data.ItemPatch{ID: "itm1", Icon: strp("")})
		if err != nil || res.ClearedImagePath != "" {
			t.Fatalf("res=%+v err=%v", res, err)
		}
		if got := f.picture(t); got != itemPhoto3584+"|-" {
			t.Fatalf("%q, want photo kept and icon NULL", got)
		}
	})
	// The generic placeholder tile is not an icon id (IDForAssetPath ""),
	// so icon "" leaves it, as before #6028; only named tiles are cleared.
	for _, tc := range []struct{ name, tile, icon, want string }{
		{"named tile", itemTile3584, "", "-|-"},
		{"named tile + stored icon", itemTile3584, "lucide:coffee", "-|-"},
		{"generic tile", "/public/assets/category-icons/generic.svg", "", "/public/assets/category-icons/generic.svg|-"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSaveFixture(t)
			ctx := context.Background()
			if err := f.catalog.SetItemPicture(ctx, "itm1", tc.tile, tc.icon); err != nil {
				t.Fatal(err)
			}
			if _, err := f.catalog.SaveItem(ctx, data.ItemPatch{ID: "itm1", Icon: strp("")}); err != nil {
				t.Fatal(err)
			}
			if got := f.picture(t); got != tc.want {
				t.Fatalf("%q, want %q", got, tc.want)
			}
		})
	}
}
