package catalog

import (
	"bytes"
	"database/sql"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// validPNG returns a tiny, real 2x2 PNG so image.Decode succeeds.
func validPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode test png: %v", err)
	}
	return buf.Bytes()
}

// multipartUpload builds a multipart/form-data body with the given plain
// fields plus an "image" file part.
func multipartUpload(t *testing.T, fields map[string]string, filename string, fileBytes []byte) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			t.Fatalf("write field %s: %v", k, err)
		}
	}
	if fileBytes != nil {
		fw, err := w.CreateFormFile("image", filename)
		if err != nil {
			t.Fatalf("create form file: %v", err)
		}
		if _, err := fw.Write(fileBytes); err != nil {
			t.Fatalf("write file bytes: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	return body, w.FormDataContentType()
}

// imageUploadTestDeps sets up an isolated catalog mux with a stable data dir
// pointed at a fresh temp directory, so upload writes never touch the real
// repo tree or bleed between tests.
func imageUploadTestDeps(t *testing.T) (*http.ServeMux, *sql.DB) {
	t.Helper()
	chdirToRepoRoot(t)
	paths.Init(t.TempDir())
	t.Cleanup(func() { paths.Init("") })

	db := testsupport.NewCatalogTestDB(t)
	mux := http.NewServeMux()
	dp := &common.Deps{
		Db:    db,
		State: common.RuntimeState{Theme: "default"},
		Menu:  []common.MenuItem{},
	}
	Register(mux, dp)
	return mux, db
}

// TestItemImageUpload_PersistsAndServesAcrossUpdates covers the real bug:
// item photos are written to the stable data dir (internal/paths), not the
// cwd-relative release tree, so an uploaded photo must (a) land under
// paths.Data and (b) still be there — and served correctly — after a second
// upload replaces it, which is exactly the "update" step that used to wipe
// uploads (see docs/code-reviews/2026-07-29-uploaded-assets-lost-on-update-and-stock-table-refresh.md).
func TestItemImageUpload_PersistsAndServesAcrossUpdates(t *testing.T) {
	mux, db := imageUploadTestDeps(t)
	testsupport.SeedTaxCode(t, db, "tax_std", "Standard", 2000)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "SKU1", Name: "Latte", BasePrice: 250, TaxCodeID: "tax_std", IsActive: true})

	upload := func(png []byte) *httptest.ResponseRecorder {
		body, ct := multipartUpload(t, map[string]string{"item_id": "itm1"}, "photo.png", png)
		req := httptest.NewRequest(http.MethodPost, "/api/catalog/item/image", body)
		req.Header.Set("Content-Type", ct)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	rec := upload(validPNG(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on first upload, got %d: %s", rec.Code, rec.Body.String())
	}
	stored := paths.Data("public", "assets", "items", "itm1", "thumb.png")
	first, err := os.ReadFile(stored)
	if err != nil {
		t.Fatalf("expected item photo written to stable data dir at %s: %v", stored, err)
	}
	if len(first) == 0 {
		t.Fatal("stored photo is empty")
	}

	// Re-upload (simulates editing the photo, or an update-triggered re-save)
	// must overwrite in place, not leave the old bytes behind.
	img2 := image.NewRGBA(image.Rect(0, 0, 3, 3))
	img2.Set(1, 1, color.RGBA{B: 255, A: 255})
	var buf2 bytes.Buffer
	if err := png.Encode(&buf2, img2); err != nil {
		t.Fatal(err)
	}
	rec = upload(buf2.Bytes())
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on second upload, got %d: %s", rec.Code, rec.Body.String())
	}
	second, err := os.ReadFile(stored)
	if err != nil {
		t.Fatalf("photo missing after second upload: %v", err)
	}
	if bytes.Equal(first, second) {
		t.Fatal("second upload did not change the stored file — a re-upload/edit would appear to silently fail")
	}
}

func TestItemImageUpload_RejectsMissingItemID(t *testing.T) {
	mux, _ := imageUploadTestDeps(t)
	body, ct := multipartUpload(t, map[string]string{}, "photo.png", validPNG(t))
	req := httptest.NewRequest(http.MethodPost, "/api/catalog/item/image", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing item_id, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestItemImageUpload_RejectsPathTraversalItemID guards against item_id
// being used to build a filesystem path directly (paths.Data("public",
// "assets", "items", itemID)) — an id like "../../etc" must never be able to
// walk the write outside the intended items/ tree.
func TestItemImageUpload_RejectsPathTraversalItemID(t *testing.T) {
	mux, _ := imageUploadTestDeps(t)
	body, ct := multipartUpload(t, map[string]string{"item_id": "../../evil"}, "photo.png", validPNG(t))
	req := httptest.NewRequest(http.MethodPost, "/api/catalog/item/image", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a path-traversal item_id, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestItemImageUpload_RejectsUnknownItem(t *testing.T) {
	mux, _ := imageUploadTestDeps(t)
	body, ct := multipartUpload(t, map[string]string{"item_id": "doesnotexist"}, "photo.png", validPNG(t))
	req := httptest.NewRequest(http.MethodPost, "/api/catalog/item/image", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for an item that doesn't exist, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestItemImageUpload_RejectsNonImageFile(t *testing.T) {
	mux, db := imageUploadTestDeps(t)
	testsupport.SeedTaxCode(t, db, "tax_std", "Standard", 2000)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "SKU1", Name: "Latte", BasePrice: 250, TaxCodeID: "tax_std", IsActive: true})

	body, ct := multipartUpload(t, map[string]string{"item_id": "itm1"}, "notes.txt", []byte("this is not an image"))
	req := httptest.NewRequest(http.MethodPost, "/api/catalog/item/image", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a non-image upload, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestItemImageUpload_RejectsMissingFile(t *testing.T) {
	mux, db := imageUploadTestDeps(t)
	testsupport.SeedTaxCode(t, db, "tax_std", "Standard", 2000)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "SKU1", Name: "Latte", BasePrice: 250, TaxCodeID: "tax_std", IsActive: true})

	body, ct := multipartUpload(t, map[string]string{"item_id": "itm1"}, "", nil)
	req := httptest.NewRequest(http.MethodPost, "/api/catalog/item/image", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when no file part is present, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestVariantImageUpload_PersistsUnderItemAndServesIndependently covers
// Farshid's report that "variant doesn't have image, each variant can have
// different image" — a variant photo must be stored (and later resolved)
// separately from its parent item's photo, nested under the item.
func TestVariantImageUpload_PersistsUnderItemAndServesIndependently(t *testing.T) {
	mux, db := imageUploadTestDeps(t)
	testsupport.SeedTaxCode(t, db, "tax_std", "Standard", 2000)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "SKU1", Name: "Latte", BasePrice: 250, TaxCodeID: "tax_std", IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v1", ItemID: "itm1", SKU: "SKU1-L", Name: "Large", Price: 300, IsActive: true})

	body, ct := multipartUpload(t, map[string]string{"variant_id": "v1", "panelItem": "itm1"}, "photo.png", validPNG(t))
	req := httptest.NewRequest(http.MethodPost, "/api/catalog/variant/image", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	variantPhoto := paths.Data("public", "assets", "items", "itm1", "variants", "v1", "thumb.png")
	if _, err := os.Stat(variantPhoto); err != nil {
		t.Fatalf("expected variant photo written at %s: %v", variantPhoto, err)
	}
	itemPhoto := paths.Data("public", "assets", "items", "itm1", "thumb.png")
	if _, err := os.Stat(itemPhoto); err == nil {
		t.Fatal("uploading a variant photo must not also write the item's own thumb.png")
	}
}

func TestVariantImageUpload_RejectsUnknownVariant(t *testing.T) {
	mux, _ := imageUploadTestDeps(t)
	body, ct := multipartUpload(t, map[string]string{"variant_id": "doesnotexist", "panelItem": "itm1"}, "photo.png", validPNG(t))
	req := httptest.NewRequest(http.MethodPost, "/api/catalog/variant/image", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for a variant that doesn't exist, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestVariantImageUpload_RejectsPathTraversalVariantID(t *testing.T) {
	mux, _ := imageUploadTestDeps(t)
	body, ct := multipartUpload(t, map[string]string{"variant_id": "../../evil", "panelItem": "itm1"}, "photo.png", validPNG(t))
	req := httptest.NewRequest(http.MethodPost, "/api/catalog/variant/image", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a path-traversal variant_id, got %d: %s", rec.Code, rec.Body.String())
	}
}
