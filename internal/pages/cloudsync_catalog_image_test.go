package pages

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/cloudsync"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/paths"
)

// set_catalog_image (ut-docs reference/manage-shop-catalog-api.md §3.9,
// ut-docs#3139): the till-side hook, driven through buildCloudHooks as Tick
// does, against a real SQLite till database and a temp data dir.

func testPNG(t *testing.T, c color.NRGBA) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 8, 6))
	for y := 0; y < 6; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func shaHex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// setImage is the directive as apply hands it to the hook: fetch returns
// body (or err) and counts its calls.
func setImage(entity, id string, body []byte, err error, calls *int) cloudsync.CatalogImage {
	return cloudsync.CatalogImage{Entity: entity, ID: id, SHA256: shaHex(body), Size: int64(len(body)),
		Fetch: func(context.Context) ([]byte, error) {
			*calls++
			return body, err
		}}
}

func itemThumbRow(t *testing.T, dp *common.Deps, itemID string) (string, bool) {
	t.Helper()
	p, ok, err := data.NewCatalogRepo(dp.Db).ItemThumbnailPath(t.Context(), itemID)
	if err != nil {
		t.Fatal(err)
	}
	return p, ok
}

func categoryImagePath(t *testing.T, dp *common.Deps, id string) (string, string) {
	t.Helper()
	var path, icon string
	if err := dp.Db.QueryRow(`SELECT COALESCE(image_path,''), COALESCE(icon,'') FROM categories WHERE id = ?`, id).Scan(&path, &icon); err != nil {
		t.Fatal(err)
	}
	return path, icon
}

func TestCloudSetCatalogImage_ItemSetReplayClear(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	hooks := buildCloudHooks(dp, nil)
	if hooks.SetCatalogImage == nil {
		t.Fatal("SetCatalogImage hook not wired")
	}
	body := testPNG(t, color.NRGBA{R: 200, A: 255})
	calls := 0
	msg, err := hooks.SetCatalogImage(ctx, setImage("item", "itm1", body, nil, &calls))
	if err != nil || msg != "image set on item Apple" || calls != 1 {
		t.Fatalf("set: %q %v (fetches %d)", msg, err, calls)
	}
	file := paths.Data("public", "assets", "items", "itm1", "thumb.png")
	written, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("thumb not written: %v", err)
	}
	if _, err := png.Decode(bytes.NewReader(written)); err != nil {
		t.Fatalf("thumb is not a PNG: %v", err)
	}
	if p, ok := itemThumbRow(t, dp, "itm1"); !ok || p != "/public/assets/items/itm1/thumb.png" {
		t.Fatalf("item_images row = %q %v", p, ok)
	}
	if n := cloudAuditCount(t, dp, "cloud_catalog_image_set", "itm1"); n != 1 {
		t.Fatalf("set audit rows = %d, want 1", n)
	}
	// The PNG my. stored is already a normalised Go PNG, so the till's
	// re-encode serves the same bytes: the snapshot's image_sha256 then
	// equals the sha my. set (my. shows its preview, not "set on the till").
	if got := cloudsync.ServedImageSHA256("/public/assets/items/itm1/thumb.png"); got != shaHex(body) {
		t.Fatalf("served sha %s, want the directive's %s", got, shaHex(body))
	}

	// A lost-result replay: same state, applied, no second audit row.
	if msg, err := hooks.SetCatalogImage(ctx, setImage("item", "itm1", body, nil, &calls)); err != nil || msg != "image set on item Apple" {
		t.Fatalf("replay: %q %v", msg, err)
	}
	if n := cloudAuditCount(t, dp, "cloud_catalog_image_set", "itm1"); n != 1 {
		t.Fatalf("a replay added an audit row: %d", n)
	}

	msg, err = hooks.SetCatalogImage(ctx, cloudsync.CatalogImage{Entity: "item", ID: "itm1", Clear: true})
	if err != nil || msg != "image removed from item Apple" {
		t.Fatalf("clear: %q %v", msg, err)
	}
	if _, ok := itemThumbRow(t, dp, "itm1"); ok {
		t.Fatal("clear left the item_images row")
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("clear left the file: %v", err)
	}
	if n := cloudAuditCount(t, dp, "cloud_catalog_image_cleared", "itm1"); n != 1 {
		t.Fatalf("clear audit rows = %d", n)
	}
	if msg, err := hooks.SetCatalogImage(ctx, cloudsync.CatalogImage{Entity: "item", ID: "itm1", Clear: true}); err != nil || msg != "image removed from item Apple" {
		t.Fatalf("clear replay: %q %v", msg, err)
	}
	if n := cloudAuditCount(t, dp, "cloud_catalog_image_cleared", "itm1"); n != 1 {
		t.Fatalf("a clear replay added an audit row: %d", n)
	}
}

func TestCloudSetCatalogImage_CategorySetClear(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	if _, err := data.NewCatalogRepo(dp.Db).SaveCategory(ctx, data.CategorySave{ID: "cat-1", Create: true, Name: sp("Drinks"), Icon: sp("lucide:coffee")}); err != nil {
		t.Fatal(err)
	}
	hooks := buildCloudHooks(dp, nil)
	body := testPNG(t, color.NRGBA{G: 180, A: 255})
	calls := 0
	msg, err := hooks.SetCatalogImage(ctx, setImage("category", "cat-1", body, nil, &calls))
	if err != nil || msg != "image set on category Drinks" {
		t.Fatalf("set: %q %v", msg, err)
	}
	// One picture per category (ut-docs#2717): the photo replaces the icon.
	if p, icon := categoryImagePath(t, dp, "cat-1"); p != "/public/assets/categories/cat-1/thumb.png" || icon != "" {
		t.Fatalf("category picture = %q / %q", p, icon)
	}
	file := paths.Data("public", "assets", "categories", "cat-1", "thumb.png")
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("category thumb not written: %v", err)
	}
	if n := cloudAuditCount(t, dp, "cloud_catalog_image_set", "cat-1"); n != 1 {
		t.Fatalf("audit rows = %d", n)
	}
	// The categories report carries the served photo's sha (rule 5).
	var shaByID = map[string]any{}
	for _, c := range remoteCategoriesReport(ctx, dp) {
		shaByID[c["id"].(string)] = c["image_sha256"]
	}
	if shaByID["cat-1"] != shaHex(body) {
		t.Fatalf("categories report image_sha256 = %v", shaByID["cat-1"])
	}

	msg, err = hooks.SetCatalogImage(ctx, cloudsync.CatalogImage{Entity: "category", ID: "cat-1", Clear: true})
	if err != nil || msg != "image removed from category Drinks" {
		t.Fatalf("clear: %q %v", msg, err)
	}
	if p, icon := categoryImagePath(t, dp, "cat-1"); p != "" || icon != "" {
		t.Fatalf("after clear = %q / %q", p, icon)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("clear left the file: %v", err)
	}
	if n := cloudAuditCount(t, dp, "cloud_catalog_image_cleared", "cat-1"); n != 1 {
		t.Fatalf("clear audit rows = %d", n)
	}
	for _, c := range remoteCategoriesReport(ctx, dp) {
		if c["id"] == "cat-1" && c["image_sha256"] != "" {
			t.Fatalf("cleared category reports image_sha256 %v", c["image_sha256"])
		}
	}
}

func TestCloudSetCatalogImage_RefusalsWriteNothing(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	hooks := buildCloudHooks(dp, nil)
	body := testPNG(t, color.NRGBA{B: 90, A: 255})
	calls := 0
	for _, c := range []struct {
		img  cloudsync.CatalogImage
		want string
	}{
		{setImage("item", "nope", body, nil, &calls), "item nope is not on this till"},
		{setImage("category", "nope", body, nil, &calls), "category nope is not on this till"},
		{cloudsync.CatalogImage{Entity: "item", ID: "nope", Clear: true}, "item nope is not on this till"},
		{cloudsync.CatalogImage{Entity: "category", ID: "nope", Clear: true}, "category nope is not on this till"},
		{setImage("variant", "itm1", body, nil, &calls), "unknown entity variant"},
	} {
		if _, err := hooks.SetCatalogImage(ctx, c.img); err == nil || err.Error() != c.want {
			t.Errorf("%s %s: %v, want %q", c.img.Entity, c.img.ID, err, c.want)
		}
	}
	if calls != 0 {
		t.Fatalf("an unknown id fetched %d images", calls)
	}
	// A failed fetch and an undecodable body fail and write nothing.
	if _, err := hooks.SetCatalogImage(ctx, setImage("item", "itm1", body, errors.New("could not download the image from the cloud (status 404); save the image again to retry"), &calls)); err == nil || !strings.Contains(err.Error(), "status 404") {
		t.Fatalf("fetch error: %v", err)
	}
	if _, err := hooks.SetCatalogImage(ctx, setImage("item", "itm1", []byte("not an image"), nil, &calls)); err == nil || err.Error() != "the image from the cloud could not be read as a PNG or JPEG" {
		t.Fatalf("bad body: %v", err)
	}
	if _, ok := itemThumbRow(t, dp, "itm1"); ok {
		t.Fatal("a refused directive wrote an item_images row")
	}
	if _, err := os.Stat(paths.Data("public", "assets", "items", "itm1", "thumb.png")); !os.IsNotExist(err) {
		t.Fatalf("a refused directive wrote a file: %v", err)
	}
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action LIKE 'cloud_catalog_image_%'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("audit rows = %d %v", n, err)
	}
}

func TestCloudSetCatalogImage_RefusedOnReplica(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	setReplica(t, dp)
	calls := 0
	_, err := buildCloudHooks(dp, nil).SetCatalogImage(t.Context(), setImage("item", "itm1", testPNG(t, color.NRGBA{A: 255}), nil, &calls))
	if err == nil || !strings.HasPrefix(err.Error(), "this data is primary-wins synced") || calls != 0 {
		t.Fatalf("replica: %v (fetches %d)", err, calls)
	}
}
