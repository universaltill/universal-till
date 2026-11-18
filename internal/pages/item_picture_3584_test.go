package pages

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/cloudsync"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/catalog"
)

// ut-docs#6028 (was #3584's replace): a save_item directive whose icon meets
// an uploaded photo, driven through buildCloudHooks exactly as Tick does —
// the photo row and its file stay and the icon is stored as the fallback,
// mirroring TestCloudSaveCategory_IconKeepsUploadedPhoto.
func TestCloudSaveItem_IconKeepsPhotoFile(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	dataDir := useTempDataDir(t)
	ctx := t.Context()
	repo := data.NewCatalogRepo(dp.Db)
	if err := repo.SetItemPhoto(ctx, "itm1", catalog.ItemThumbURL("itm1")); err != nil {
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
	if p, ok := itemThumbRow(t, dp, "itm1"); !ok || p != catalog.ItemThumbURL("itm1") {
		t.Fatalf("the icon must keep the item_images thumbnail row, got %q %v", p, ok)
	}
	if icon, err := repo.ItemIcon(ctx, "itm1"); err != nil || icon != "lucide:beer" {
		t.Fatalf("icon = %q %v", icon, err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("the photo file must stay, stat err=%v", err)
	}
}

// ut-docs#3601 reverses #3584 here: set_catalog_image clear removes only the
// image, as for a category. On an icon-only item it is a no-op (no audit).
func TestCloudSetCatalogImage_ClearKeepsIconOnlyItem(t *testing.T) {
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
	if icon, _ := repo.ItemIcon(ctx, "itm1"); icon != "lucide:beer" {
		t.Fatalf("icon = %q after clear, want lucide:beer kept", icon)
	}
	if n := cloudAuditCount(t, dp, "cloud_catalog_image_cleared", "itm1"); n != 0 {
		t.Fatalf("clear audit rows = %d, want 0", n)
	}
}
