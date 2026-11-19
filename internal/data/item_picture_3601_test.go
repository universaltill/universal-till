package data_test

import (
	"context"
	"errors"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/iconid"
)

// ut-docs#3601: an uploaded photo keeps the item's stored icon (the item
// counterpart of #3585's category rule); the till picker's library pick and
// "No image" still replace / clear both.
func TestSetItemPhoto_KeepsIcon(t *testing.T) {
	tileID := iconid.IDForAssetPath(itemTile3584)
	if tileID == "" {
		t.Fatal("test tile has no icon id")
	}
	type op func(f saveFixture, ctx context.Context) error
	setPhoto := func(f saveFixture, ctx context.Context) error {
		return f.catalog.SetItemPhoto(ctx, "itm1", itemPhoto3584)
	}
	clearPhoto := func(f saveFixture, ctx context.Context) error { return f.catalog.ClearItemPhoto(ctx, "itm1") }
	cases := []struct {
		name  string
		thumb string
		icon  string
		op    op
		want  string
	}{
		{"icon only + photo", "", "lucide:beer", setPhoto, itemPhoto3584 + "|lucide:beer"},
		{"tile only + photo", itemTile3584, "", setPhoto, itemPhoto3584 + "|" + tileID},
		{"tile and icon + photo", itemTile3584, "lucide:coffee", setPhoto, itemPhoto3584 + "|lucide:coffee"},
		{"nothing + photo", "", "", setPhoto, itemPhoto3584 + "|-"},
		{"replay", itemPhoto3584, "lucide:beer", setPhoto, itemPhoto3584 + "|lucide:beer"},
		{"clear photo+icon", itemPhoto3584, "lucide:beer", clearPhoto, "-|lucide:beer"},
		{"clear tile only", itemTile3584, "", clearPhoto, "-|" + tileID},
		{"clear icon only", "", "lucide:beer", clearPhoto, "-|lucide:beer"},
		{"clear nothing", "", "", clearPhoto, "-|-"},
		{"till No image clears both", itemPhoto3584, "lucide:beer", func(f saveFixture, ctx context.Context) error {
			return f.catalog.ClearItemThumbnail(ctx, "itm1")
		}, "-|-"},
		{"till library pick replaces both", itemPhoto3584, "lucide:beer", func(f saveFixture, ctx context.Context) error {
			return f.catalog.SetItemThumbnail(ctx, "itm1", itemTile3584)
		}, itemTile3584 + "|-"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newSaveFixture(t)
			ctx := context.Background()
			if c.thumb != "" {
				f.exec(t, `INSERT INTO item_images (id, item_id, path, role) VALUES ('img1', 'itm1', ?, 'thumbnail')`, c.thumb)
			}
			if c.icon != "" {
				f.exec(t, `UPDATE items SET icon = ? WHERE id = 'itm1'`, c.icon)
			}
			if err := c.op(f, ctx); err != nil {
				t.Fatal(err)
			}
			if got := f.picture(t); got != c.want {
				t.Fatalf("picture = %q, want %q", got, c.want)
			}
			if got := f.str(t, `SELECT COUNT(*) FROM item_images WHERE item_id = 'itm1' AND role = 'thumbnail'`); got != "0" && got != "1" {
				t.Fatalf("thumbnail rows = %s", got)
			}
		})
	}
}

func TestSetItemPhoto_Refusals(t *testing.T) {
	f := newSaveFixture(t)
	ctx := context.Background()
	f.exec(t, `UPDATE items SET icon = 'lucide:beer' WHERE id = 'itm1'`)
	if err := f.catalog.SetItemPhoto(ctx, "itm1", itemTile3584); err == nil {
		t.Fatal("a library tile path must be refused")
	}
	if err := f.catalog.SetItemPhoto(ctx, "itm1", ""); err == nil {
		t.Fatal("an empty path must be refused")
	}
	if got := f.picture(t); got != "-|lucide:beer" {
		t.Fatalf("refused writes changed the item: %q", got)
	}
	if err := f.catalog.SetItemPhoto(ctx, "nope", itemPhoto3584); !errors.Is(err, data.ErrItemNotFound) {
		t.Fatalf("SetItemPhoto unknown: %v", err)
	}
	if err := f.catalog.ClearItemPhoto(ctx, "nope"); !errors.Is(err, data.ErrItemNotFound) {
		t.Fatalf("ClearItemPhoto unknown: %v", err)
	}
}

func TestSetItemPicture_BothStored(t *testing.T) {
	f := newSaveFixture(t)
	ctx := context.Background()
	if err := f.catalog.SetItemPicture(ctx, "itm1", itemPhoto3584, "lucide:beer"); err != nil {
		t.Fatal(err)
	}
	if got := f.picture(t); got != itemPhoto3584+"|lucide:beer" {
		t.Fatalf("both: %q", got)
	}
	if err := f.catalog.SetItemPicture(ctx, "itm1", "", ""); err != nil {
		t.Fatal(err)
	}
	if got := f.picture(t); got != "-|-" {
		t.Fatalf("none: %q", got)
	}
}
