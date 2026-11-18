package pages

import (
	"image/color"
	"os"
	"testing"

	"github.com/universaltill/universal-till/internal/cloudsync"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/iconid"
	"github.com/universaltill/universal-till/internal/paths"
)

// ut-docs#3601: set_catalog_image on an item keeps the stored icon, set or
// clear; only the image decides replays.
func TestCloudSetCatalogImage_ItemPhotoKeepsIcon(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	repo := data.NewCatalogRepo(dp.Db)
	if err := repo.SetItemPicture(ctx, "itm1", "", "lucide:beer"); err != nil {
		t.Fatal(err)
	}
	hooks := buildCloudHooks(dp, nil)
	body := testPNG(t, color.NRGBA{R: 90, A: 255})
	calls := 0
	msg, err := hooks.SetCatalogImage(ctx, setImage("item", "itm1", body, nil, &calls))
	if err != nil || msg != "image set on item Apple" {
		t.Fatalf("set: %q %v", msg, err)
	}
	if p, ok := itemThumbRow(t, dp, "itm1"); !ok || p != "/public/assets/items/itm1/thumb.png" {
		t.Fatalf("row = %q %v", p, ok)
	}
	if icon, _ := repo.ItemIcon(ctx, "itm1"); icon != "lucide:beer" {
		t.Fatalf("icon after set = %q, want lucide:beer", icon)
	}
	if n := cloudAuditCount(t, dp, "cloud_catalog_image_set", "itm1"); n != 1 {
		t.Fatalf("set audit = %d", n)
	}
	if _, err := hooks.SetCatalogImage(ctx, setImage("item", "itm1", body, nil, &calls)); err != nil {
		t.Fatal(err)
	}
	if n := cloudAuditCount(t, dp, "cloud_catalog_image_set", "itm1"); n != 1 {
		t.Fatalf("set replay audit = %d, want 1", n)
	}
	file := paths.Data("public", "assets", "items", "itm1", "thumb.png")
	clr := cloudsync.CatalogImage{Entity: "item", ID: "itm1", Clear: true}
	if msg, err = hooks.SetCatalogImage(ctx, clr); err != nil || msg != "image removed from item Apple" {
		t.Fatalf("clear: %q %v", msg, err)
	}
	if _, ok := itemThumbRow(t, dp, "itm1"); ok {
		t.Fatal("clear left the row")
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("clear left the file: %v", err)
	}
	if icon, _ := repo.ItemIcon(ctx, "itm1"); icon != "lucide:beer" {
		t.Fatalf("icon after clear = %q, want lucide:beer", icon)
	}
	if n := cloudAuditCount(t, dp, "cloud_catalog_image_cleared", "itm1"); n != 1 {
		t.Fatalf("clear audit = %d", n)
	}
	if _, err := hooks.SetCatalogImage(ctx, clr); err != nil {
		t.Fatal(err)
	}
	if n := cloudAuditCount(t, dp, "cloud_catalog_image_cleared", "itm1"); n != 1 {
		t.Fatalf("clear replay audit = %d, want 1", n)
	}
}

func TestCloudSetCatalogImage_ItemPhotoOverLibraryTile(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	repo := data.NewCatalogRepo(dp.Db)
	const tile = "/public/assets/category-icons/beer.svg"
	tileID := iconid.IDForAssetPath(tile)
	if tileID == "" {
		t.Fatal("tile has no id")
	}
	if err := repo.SetItemThumbnail(ctx, "itm1", tile); err != nil {
		t.Fatal(err)
	}
	hooks := buildCloudHooks(dp, nil)
	calls := 0
	body := testPNG(t, color.NRGBA{B: 120, A: 255})
	if _, err := hooks.SetCatalogImage(ctx, setImage("item", "itm1", body, nil, &calls)); err != nil {
		t.Fatal(err)
	}
	if icon, _ := repo.ItemIcon(ctx, "itm1"); icon != tileID {
		t.Fatalf("icon after set = %q, want %q", icon, tileID)
	}
	if _, err := hooks.SetCatalogImage(ctx, cloudsync.CatalogImage{Entity: "item", ID: "itm1", Clear: true}); err != nil {
		t.Fatal(err)
	}
	if _, ok := itemThumbRow(t, dp, "itm1"); ok {
		t.Fatal("clear left the row")
	}
	if icon, _ := repo.ItemIcon(ctx, "itm1"); icon != tileID {
		t.Fatalf("icon after clear = %q, want %q", icon, tileID)
	}
}
