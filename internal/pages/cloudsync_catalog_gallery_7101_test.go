package pages

import (
	"context"
	"image/color"
	"os"
	"testing"

	"github.com/universaltill/universal-till/internal/cloudsync"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/paths"
)

// ut-docs#7101 (ADR-0218): the till side of set_catalog_image v2.

func galleryDirective(t *testing.T, id string, cols ...color.NRGBA) (cloudsync.CatalogImage, *int) {
	t.Helper()
	calls := new(int)
	img := cloudsync.CatalogImage{Entity: "item", ID: id}
	for i, c := range cols {
		body := testPNG(t, c)
		ref := cloudsync.CatalogImageRef{SHA256: shaHex(body), Alt: []string{"front", "back", "side"}[i%3],
			Fetch: func(context.Context) ([]byte, error) { *calls++; return body, nil }}
		img.Images = append(img.Images, ref)
	}
	img.SHA256, img.Fetch = img.Images[0].SHA256, img.Images[0].Fetch
	return img, calls
}

func TestSetCatalogImageV2_ReplacesGallery(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	hooks := buildCloudHooks(dp, nil)
	red, green, blue := color.NRGBA{R: 200, A: 255}, color.NRGBA{G: 200, A: 255}, color.NRGBA{B: 200, A: 255}
	d, calls := galleryDirective(t, "itm1", red, green, blue)
	msg, err := hooks.SetCatalogImage(ctx, d)
	if err != nil || msg != "images set on item Apple" || *calls != 3 {
		t.Fatalf("set: %q %v fetches %d", msg, err, *calls)
	}
	repo := data.NewCatalogRepo(dp.Db)
	g, err := repo.ItemGallery(ctx, "itm1")
	if err != nil || len(g) != 3 {
		t.Fatalf("gallery %+v %v", g, err)
	}
	if g[0].Path != "/public/assets/items/itm1/thumb.png" || g[0].Alt != "front" || g[1].Alt != "back" || g[2].Alt != "side" {
		t.Fatalf("gallery %+v", g)
	}
	for _, x := range g {
		f := paths.Data("public", "assets", x.Path[len("/public/assets/"):])
		if _, err := os.Stat(f); err != nil {
			t.Errorf("file for %s missing: %v", x.Path, err)
		}
	}
	if got := cloudsync.ServedImageSHA256(g[0].Path); got != d.SHA256 {
		t.Fatalf("primary served sha %s want %s", got, d.SHA256)
	}
	if n := cloudAuditCount(t, dp, "cloud_catalog_image_set", "itm1"); n != 1 {
		t.Fatalf("audit %d", n)
	}
	// replay: same state, no second audit row
	d2, _ := galleryDirective(t, "itm1", red, green, blue)
	if _, err := hooks.SetCatalogImage(ctx, d2); err != nil {
		t.Fatal(err)
	}
	if n := cloudAuditCount(t, dp, "cloud_catalog_image_set", "itm1"); n != 1 {
		t.Fatalf("replay audit %d", n)
	}
	// shrink to one picture: no leftovers (rows and files)
	d3, _ := galleryDirective(t, "itm1", green)
	if _, err := hooks.SetCatalogImage(ctx, d3); err != nil {
		t.Fatal(err)
	}
	g, _ = repo.ItemGallery(ctx, "itm1")
	if len(g) != 1 {
		t.Fatalf("after shrink %+v", g)
	}
	left, _ := os.ReadDir(paths.Data("public", "assets", "items", "itm1"))
	if len(left) != 1 {
		t.Fatalf("files left: %v", left)
	}
}

func TestSetCatalogImageV2_FetchFailureChangesNothing(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	hooks := buildCloudHooks(dp, nil)
	d, _ := galleryDirective(t, "itm1", color.NRGBA{R: 9, A: 255}, color.NRGBA{G: 9, A: 255})
	d.Images[1].Fetch = func(context.Context) ([]byte, error) { return nil, os.ErrDeadlineExceeded }
	if _, err := hooks.SetCatalogImage(ctx, d); err == nil {
		t.Fatal("a failed fetch must fail the directive")
	}
	if g, _ := data.NewCatalogRepo(dp.Db).ItemGallery(ctx, "itm1"); len(g) != 0 {
		t.Fatalf("partial gallery written: %+v", g)
	}
}

func TestSetCatalogImageV1_StillWorksAndClearDropsGallery(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	hooks := buildCloudHooks(dp, nil)
	d, _ := galleryDirective(t, "itm1", color.NRGBA{R: 1, A: 255}, color.NRGBA{G: 1, A: 255})
	if _, err := hooks.SetCatalogImage(ctx, d); err != nil {
		t.Fatal(err)
	}
	calls := 0
	if msg, err := hooks.SetCatalogImage(ctx, setImage("item", "itm1", testPNG(t, color.NRGBA{B: 5, A: 255}), nil, &calls)); err != nil || msg != "image set on item Apple" {
		t.Fatalf("v1: %q %v", msg, err)
	}
	if _, err := hooks.SetCatalogImage(ctx, cloudsync.CatalogImage{Entity: "item", ID: "itm1", Clear: true}); err != nil {
		t.Fatal(err)
	}
	if g, _ := data.NewCatalogRepo(dp.Db).ItemGallery(ctx, "itm1"); len(g) != 0 {
		t.Fatalf("clear left %+v", g)
	}
	if left, _ := os.ReadDir(paths.Data("public", "assets", "items", "itm1")); len(left) != 0 {
		t.Fatalf("clear left files %v", left)
	}
}

func TestSetCatalogImageV2_VariantGallery(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	hooks := buildCloudHooks(dp, nil)
	if _, err := dp.Db.Exec(`INSERT INTO item_variants (id, item_id, sku, name, price) VALUES ('galvar1','itm1','GALV1','Red',100)`); err != nil {
		t.Fatal(err)
	}
	d, _ := galleryDirective(t, "itm1", color.NRGBA{R: 7, A: 255}, color.NRGBA{G: 7, A: 255})
	d.VariantID = "galvar1"
	if _, err := hooks.SetCatalogImage(ctx, d); err != nil {
		t.Fatal(err)
	}
	repo := data.NewCatalogRepo(dp.Db)
	vg, _ := repo.VariantGallery(ctx, "itm1", "galvar1")
	if len(vg) != 2 || vg[0].Alt != "front" {
		t.Fatalf("variant gallery %+v", vg)
	}
	if _, ok, _ := repo.ItemThumbnailPath(ctx, "itm1"); ok {
		t.Fatal("variant gallery changed the item thumbnail")
	}
	d.VariantID = "nope"
	if _, err := hooks.SetCatalogImage(ctx, d); err == nil {
		t.Fatal("unknown variant must fail")
	}
}
