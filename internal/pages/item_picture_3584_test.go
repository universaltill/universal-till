package pages

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/cloudsync"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/catalog"
)

// ut-docs#3584: a save_item directive whose icon replaces an uploaded
// photo, driven through buildCloudHooks exactly as Tick does — the item
// keeps one picture (the icon) and the superseded photo's file is removed,
// mirroring TestCloudSaveCategory_IconReplacesUploadedPhoto.
func TestCloudSaveItem_IconReplacesUploadedPhoto(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	dataDir := useTempDataDir(t)
	ctx := t.Context()
	repo := data.NewCatalogRepo(dp.Db)
	if err := repo.SetItemThumbnail(ctx, "itm1", catalog.ItemThumbURL("itm1")); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dataDir, "public", "assets", "items", "itm1", "thumb.png")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, tinyPNG(t), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := buildCloudHooks(dp, nil).SaveItem(ctx, data.ItemPatch{ID: "itm1", Icon: sp("lucide:beer")}); err != nil {
		t.Fatal(err)
	}
	if _, ok := itemThumbRow(t, dp, "itm1"); ok {
		t.Fatal("the icon must replace the item_images thumbnail row")
	}
	if icon, err := repo.ItemIcon(ctx, "itm1"); err != nil || icon != "lucide:beer" {
		t.Fatalf("icon = %q %v", icon, err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("the superseded upload must be removed, stat err=%v", err)
	}
}

// set_catalog_image's clear on an item that shows an icon (no thumbnail
// row) is not a no-op replay: the icon is the item's picture, and "remove"
// takes it away, as for a category.
func TestCloudSetCatalogImage_ClearRemovesItemIcon(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	repo := data.NewCatalogRepo(dp.Db)
	if err := repo.SetItemPicture(ctx, "itm1", "", "lucide:beer"); err != nil {
		t.Fatal(err)
	}
	msg, err := buildCloudHooks(dp, nil).SetCatalogImage(ctx, cloudsync.CatalogImage{Entity: "item", ID: "itm1", Clear: true})
	if err != nil || msg != "image removed from item Apple" {
		t.Fatalf("clear: %q %v", msg, err)
	}
	if icon, _ := repo.ItemIcon(ctx, "itm1"); icon != "" {
		t.Fatalf("icon = %q after clear, want none", icon)
	}
	if n := cloudAuditCount(t, dp, "cloud_catalog_image_cleared", "itm1"); n != 1 {
		t.Fatalf("clear audit rows = %d, want 1", n)
	}
}
