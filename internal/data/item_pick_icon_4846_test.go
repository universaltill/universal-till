package data_test

import (
	"context"
	"errors"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/iconid"
)

// ut-docs#4846 (owner decision C): the till picker's library pick keeps an
// uploaded photo and stores the pick as items.icon, the photo's fallback.

const tileGeneric4846 = "/public/assets/category-icons/generic.svg"

func TestPickItemIcon_PhotoKeptIconStoredAsFallback(t *testing.T) {
	f := newSaveFixture(t)
	ctx := context.Background()
	if err := f.catalog.SetItemPhoto(ctx, "itm1", itemPhoto3584); err != nil {
		t.Fatal(err)
	}
	kept, err := f.catalog.PickItemIcon(ctx, "itm1", itemTile3584)
	if err != nil || !kept {
		t.Fatalf("kept=%v err=%v, want true,nil", kept, err)
	}
	if got := f.picture(t); got != itemPhoto3584+"|lucide:beer" {
		t.Fatalf("picture = %q, want photo row kept and icon set", got)
	}
	// Idempotent: same pick twice, still one thumbnail row.
	if _, err := f.catalog.PickItemIcon(ctx, "itm1", itemTile3584); err != nil {
		t.Fatal(err)
	}
	if got := f.picture(t); got != itemPhoto3584+"|lucide:beer" {
		t.Fatalf("after repeat: %q", got)
	}
	if n := f.str(t, `SELECT COUNT(*) FROM item_images WHERE item_id='itm1' AND role='thumbnail'`); n != "1" {
		t.Fatalf("thumbnail rows = %s, want 1", n)
	}
	// The photo still wins; removing it surfaces the fallback (AC4).
	thumb, _, _ := f.catalog.ItemThumbnailPath(ctx, "itm1")
	icon, _ := f.catalog.ItemIcon(ctx, "itm1")
	if p := iconid.Picture(thumb, icon); p != itemPhoto3584 {
		t.Fatalf("Picture = %q, want the photo", p)
	}
	if err := f.catalog.ClearItemPhoto(ctx, "itm1"); err != nil {
		t.Fatal(err)
	}
	thumb, _, _ = f.catalog.ItemThumbnailPath(ctx, "itm1")
	icon, _ = f.catalog.ItemIcon(ctx, "itm1")
	if p := iconid.Picture(thumb, icon); p != itemTile3584 {
		t.Fatalf("Picture after photo removed = %q, want %q", p, itemTile3584)
	}
}

func TestPickItemIcon_GenericOnPhotoWritesNothing(t *testing.T) {
	f := newSaveFixture(t)
	ctx := context.Background()
	if err := f.catalog.SetItemPicture(ctx, "itm1", itemPhoto3584, "lucide:beer"); err != nil {
		t.Fatal(err)
	}
	kept, err := f.catalog.PickItemIcon(ctx, "itm1", tileGeneric4846)
	if err != nil || !kept {
		t.Fatalf("kept=%v err=%v, want true,nil", kept, err)
	}
	if got := f.picture(t); got != itemPhoto3584+"|lucide:beer" {
		t.Fatalf("picture = %q, want unchanged", got)
	}
}

func TestPickItemIcon_NoPhotoReplacesLikeBefore(t *testing.T) {
	f := newSaveFixture(t)
	ctx := context.Background()
	// No thumbnail row, stale icon: tile written, icon cleared.
	f.exec(t, `UPDATE items SET icon = 'lucide:coffee' WHERE id = 'itm1'`)
	kept, err := f.catalog.PickItemIcon(ctx, "itm1", itemTile3584)
	if err != nil || kept {
		t.Fatalf("kept=%v err=%v, want false,nil", kept, err)
	}
	if got := f.picture(t); got != itemTile3584+"|-" {
		t.Fatalf("picture = %q", got)
	}
	// A tile row is replaced by the new tile.
	if kept, err = f.catalog.PickItemIcon(ctx, "itm1", tileGeneric4846); err != nil || kept {
		t.Fatalf("kept=%v err=%v", kept, err)
	}
	if got := f.picture(t); got != tileGeneric4846+"|-" {
		t.Fatalf("picture = %q", got)
	}
}

func TestPickItemIcon_Refusals(t *testing.T) {
	f := newSaveFixture(t)
	ctx := context.Background()
	if _, err := f.catalog.PickItemIcon(ctx, "nope", itemTile3584); !errors.Is(err, data.ErrItemNotFound) {
		t.Fatalf("unknown item: %v, want ErrItemNotFound", err)
	}
	if _, err := f.catalog.PickItemIcon(ctx, "itm1", itemPhoto3584); err == nil {
		t.Fatal("a non-tile path must be refused")
	}
	if got := f.picture(t); got != "-|-" {
		t.Fatalf("refusal wrote: %q", got)
	}
}
