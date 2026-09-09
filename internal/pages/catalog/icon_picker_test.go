package catalog

// ut-docs#1844: the catalog add/edit item flow can assign an item's
// thumbnail from the existing bundled built-in icons (catimport.
// BuiltinIcons), not only via a custom upload — both go through the same
// item_images/thumbnail row, so every item_images-driven reader (POS grid,
// basket, search, suggestions) needs no change to pick either up. The
// self-order kiosk is a known exception (self_order_shop.go resolves an
// item's photo by path CONVENTION, not via item_images — see that file's
// own comment) and is NOT covered by this card; ut-docs#1189 already
// documents the same split for the import-placeholder icon.

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// iconPickerFileTestDeps is newCatalogMux plus paths.Init sandboxing
// (review finding F1, ut-docs#1844): choosing/clearing a built-in icon now
// also removes an item's uploaded thumbnail file, so a test touching that
// needs paths.Data(...) pointed at a throwaway temp dir, same as
// image_upload_test.go's imageUploadTestDeps — every other test in this
// file uses the plain newCatalogMux since it never touches the filesystem.
func iconPickerFileTestDeps(t *testing.T) (*http.ServeMux, *sql.DB) {
	t.Helper()
	mux, db := newCatalogMux(t)
	paths.Init(t.TempDir())
	t.Cleanup(func() { paths.Init("") })
	return mux, db
}

func TestIconState_MissingItemID(t *testing.T) {
	mux, _ := newCatalogMux(t)
	rec := get(t, mux, "/api/catalog/item/icon-state")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing item_id, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestIconState_UnknownItem(t *testing.T) {
	mux, _ := newCatalogMux(t)
	rec := get(t, mux, "/api/catalog/item/icon-state?item_id=doesnotexist")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for an unknown item, got %d: %s", rec.Code, rec.Body.String())
	}
}

type iconStateBody struct {
	Data struct {
		SelectedKey  string `json:"selected_key"`
		IsCustom     bool   `json:"is_custom"`
		SuggestedKey string `json:"suggested_key"`
	} `json:"data"`
}

// TestIconState_ImagelessItemSuggestsFromNameKeyword pins the "preselected
// suggestion" acceptance criterion: an item with no thumbnail at all gets
// a suggested_key from the same keyword match PlaceholderIcon already uses
// for imports, and no selected_key (nothing is actually chosen yet).
func TestIconState_ImagelessItemSuggestsFromNameKeyword(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "SKU1", Name: "Cappuccino", BasePrice: 250, IsActive: true})

	rec := get(t, mux, "/api/catalog/item/icon-state?item_id=itm1")
	if rec.Code != http.StatusOK {
		t.Fatalf("icon-state: code %d body %s", rec.Code, rec.Body.String())
	}
	var body iconStateBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if body.Data.SuggestedKey != "coffee" {
		t.Errorf("suggested_key = %q, want coffee", body.Data.SuggestedKey)
	}
	if body.Data.SelectedKey != "" || body.Data.IsCustom {
		t.Errorf("expected no current selection for an imageless item, got selected_key=%q is_custom=%v", body.Data.SelectedKey, body.Data.IsCustom)
	}
}

// TestIconState_ReportsCurrentBuiltinSelection: an item already carrying a
// built-in icon (set by this card's own POST /api/catalog/item/icon, or by
// the import placeholder) reports that key back, not just a suggestion.
func TestIconState_ReportsCurrentBuiltinSelection(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "SKU1", Name: "Bananas", BasePrice: 150, IsActive: true})
	if _, err := db.Exec(`INSERT INTO item_images (id, item_id, path, role) VALUES ('img1','itm1','/public/assets/category-icons/drink.svg','thumbnail')`); err != nil {
		t.Fatalf("seed thumbnail: %v", err)
	}

	rec := get(t, mux, "/api/catalog/item/icon-state?item_id=itm1")
	if rec.Code != http.StatusOK {
		t.Fatalf("icon-state: code %d body %s", rec.Code, rec.Body.String())
	}
	var body iconStateBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if body.Data.SelectedKey != "drink" || body.Data.IsCustom {
		t.Errorf("selected_key=%q is_custom=%v, want drink/false", body.Data.SelectedKey, body.Data.IsCustom)
	}
}

// TestIconState_ReportsCustomUpload: a thumbnail path that isn't one of
// the bundled built-in paths (a real uploaded photo) reports is_custom,
// with no selected built-in key — the two are mutually exclusive.
func TestIconState_ReportsCustomUpload(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "SKU1", Name: "Latte", BasePrice: 250, IsActive: true})
	if _, err := db.Exec(`INSERT INTO item_images (id, item_id, path, role) VALUES ('img1','itm1','/public/assets/items/itm1/thumb.png','thumbnail')`); err != nil {
		t.Fatalf("seed thumbnail: %v", err)
	}

	rec := get(t, mux, "/api/catalog/item/icon-state?item_id=itm1")
	if rec.Code != http.StatusOK {
		t.Fatalf("icon-state: code %d body %s", rec.Code, rec.Body.String())
	}
	var body iconStateBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if !body.Data.IsCustom || body.Data.SelectedKey != "" {
		t.Errorf("selected_key=%q is_custom=%v, want \"\"/true for a custom photo", body.Data.SelectedKey, body.Data.IsCustom)
	}
}

func TestChooseIcon_MissingItemID(t *testing.T) {
	mux, _ := newCatalogMux(t)
	rec := postForm(t, mux, "/api/catalog/item/icon", "icon=coffee")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing item_id, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestChooseIcon_UnknownItem(t *testing.T) {
	mux, _ := newCatalogMux(t)
	rec := postForm(t, mux, "/api/catalog/item/icon", "item_id=doesnotexist&icon=coffee")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for an unknown item, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestChooseIcon_RejectsUnknownIconKey(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "SKU1", Name: "Item", BasePrice: 100, IsActive: true})
	rec := postForm(t, mux, "/api/catalog/item/icon", "item_id=itm1&icon=not-a-real-icon")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unknown icon key, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestChooseIcon_SetsBuiltinThumbnail is the picker's main path: choosing
// a built-in icon stores it exactly like an upload (item_images/thumbnail,
// same table/role), and the response is a row OOB fragment, same protocol
// as every other catalog mutation (ut-docs#1363) — never the whole table.
func TestChooseIcon_SetsBuiltinThumbnail(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "SKU1", Name: "Item", BasePrice: 100, IsActive: true})

	rec := postForm(t, mux, "/api/catalog/item/icon", "item_id=itm1&icon=coffee")
	if rec.Code != http.StatusOK {
		t.Fatalf("choose icon: code %d body %s", rec.Code, rec.Body.String())
	}
	assertNoFullTable(t, rec.Body.String())

	var path string
	if err := db.QueryRow(`SELECT path FROM item_images WHERE item_id = 'itm1' AND role = 'thumbnail'`).Scan(&path); err != nil {
		t.Fatalf("expected a thumbnail row: %v", err)
	}
	if path != "/public/assets/category-icons/coffee.svg" {
		t.Errorf("path = %q, want the coffee icon path", path)
	}
}

// TestChooseIcon_ReplacesACustomUpload: choosing a built-in icon after a
// photo was already uploaded must replace it, not add a second row — the
// two are mutually exclusive, same as SetItemThumbnail's existing
// overwrite behaviour for uploads.
func TestChooseIcon_ReplacesACustomUpload(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "SKU1", Name: "Item", BasePrice: 100, IsActive: true})
	if _, err := db.Exec(`INSERT INTO item_images (id, item_id, path, role) VALUES ('img1','itm1','/public/assets/items/itm1/thumb.png','thumbnail')`); err != nil {
		t.Fatalf("seed thumbnail: %v", err)
	}

	rec := postForm(t, mux, "/api/catalog/item/icon", "item_id=itm1&icon=pastry")
	if rec.Code != http.StatusOK {
		t.Fatalf("choose icon: code %d body %s", rec.Code, rec.Body.String())
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM item_images WHERE item_id = 'itm1' AND role = 'thumbnail'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 thumbnail row, got %d", count)
	}
	var path string
	if err := db.QueryRow(`SELECT path FROM item_images WHERE item_id = 'itm1' AND role = 'thumbnail'`).Scan(&path); err != nil {
		t.Fatal(err)
	}
	if path != "/public/assets/category-icons/pastry.svg" {
		t.Errorf("path = %q, want the pastry icon path to have replaced the upload", path)
	}
}

// TestChooseIcon_EmptyIconClearsThumbnail is the "clearing back to none"
// acceptance criterion: an empty (or "none") icon value removes the
// thumbnail row entirely, whether it was previously a built-in icon or a
// custom upload.
func TestChooseIcon_EmptyIconClearsThumbnail(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "SKU1", Name: "Item", BasePrice: 100, IsActive: true})
	if _, err := db.Exec(`INSERT INTO item_images (id, item_id, path, role) VALUES ('img1','itm1','/public/assets/category-icons/coffee.svg','thumbnail')`); err != nil {
		t.Fatalf("seed thumbnail: %v", err)
	}

	rec := postForm(t, mux, "/api/catalog/item/icon", "item_id=itm1&icon=none")
	if rec.Code != http.StatusOK {
		t.Fatalf("clear icon: code %d body %s", rec.Code, rec.Body.String())
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM item_images WHERE item_id = 'itm1' AND role = 'thumbnail'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expected the thumbnail row to be cleared, got %d rows", count)
	}
}

// TestChooseIcon_RejectsPathTraversalItemID guards the same class of bug
// the sibling image-upload handler already tests for: an item_id used to
// build a filesystem path (removeUploadedThumbnail, F1 below) must never
// be able to walk that path outside the intended items/ tree.
func TestChooseIcon_RejectsPathTraversalItemID(t *testing.T) {
	mux, _ := newCatalogMux(t)
	rec := postForm(t, mux, "/api/catalog/item/icon", "item_id=../../evil&icon=coffee")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a path-traversal item_id, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestChooseIcon_RemovesUploadedThumbnailFile is review finding F1
// (ut-docs#1844): choosing a built-in icon over an existing uploaded photo
// must remove the now-superseded FILE, not just repoint item_images —
// catalog_row.html/catalog_variants.html's imgExists check and
// self_order_shop.go's hardcoded ImageURL both resolve a photo by the
// "items/<id>/thumb.png" path CONVENTION regardless of what item_images
// says, so a stale file there would keep showing the old photo on those
// surfaces even after the operator picked a different image.
func TestChooseIcon_RemovesUploadedThumbnailFile(t *testing.T) {
	mux, db := iconPickerFileTestDeps(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "SKU1", Name: "Item", BasePrice: 100, IsActive: true})

	thumbPath := filepath.Join(paths.Data("public", "assets", "items", "itm1"), "thumb.png")
	if err := os.MkdirAll(filepath.Dir(thumbPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(thumbPath, []byte("fake-png-bytes"), 0o644); err != nil {
		t.Fatalf("seed uploaded file: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO item_images (id, item_id, path, role) VALUES ('img1','itm1','/public/assets/items/itm1/thumb.png','thumbnail')`); err != nil {
		t.Fatalf("seed thumbnail row: %v", err)
	}

	rec := postForm(t, mux, "/api/catalog/item/icon", "item_id=itm1&icon=coffee")
	if rec.Code != http.StatusOK {
		t.Fatalf("choose icon: code %d body %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(thumbPath); !os.IsNotExist(err) {
		t.Fatalf("expected the superseded uploaded file to be removed, stat err = %v", err)
	}
}

// TestChooseIcon_ClearRemovesUploadedThumbnailFile is F1's clear-path
// twin: "No image" must remove a previously-uploaded file too, not just
// the item_images row — otherwise the convention-based readers above keep
// showing a photo the operator explicitly cleared.
func TestChooseIcon_ClearRemovesUploadedThumbnailFile(t *testing.T) {
	mux, db := iconPickerFileTestDeps(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "SKU1", Name: "Item", BasePrice: 100, IsActive: true})

	thumbPath := filepath.Join(paths.Data("public", "assets", "items", "itm1"), "thumb.png")
	if err := os.MkdirAll(filepath.Dir(thumbPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(thumbPath, []byte("fake-png-bytes"), 0o644); err != nil {
		t.Fatalf("seed uploaded file: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO item_images (id, item_id, path, role) VALUES ('img1','itm1','/public/assets/items/itm1/thumb.png','thumbnail')`); err != nil {
		t.Fatalf("seed thumbnail row: %v", err)
	}

	rec := postForm(t, mux, "/api/catalog/item/icon", "item_id=itm1&icon=none")
	if rec.Code != http.StatusOK {
		t.Fatalf("clear icon: code %d body %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(thumbPath); !os.IsNotExist(err) {
		t.Fatalf("expected the cleared uploaded file to be removed, stat err = %v", err)
	}
}

// TestChooseIcon_NoUploadedFileIsNotAnError: clearing/choosing on an item
// that never had a real uploaded file (only ever a built-in icon, or
// nothing) must not error just because there's no file to remove —
// removeUploadedThumbnail treats os.IsNotExist as a non-error.
func TestChooseIcon_NoUploadedFileIsNotAnError(t *testing.T) {
	mux, db := iconPickerFileTestDeps(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "SKU1", Name: "Item", BasePrice: 100, IsActive: true})

	rec := postForm(t, mux, "/api/catalog/item/icon", "item_id=itm1&icon=drink")
	if rec.Code != http.StatusOK {
		t.Fatalf("choose icon: code %d body %s", rec.Code, rec.Body.String())
	}
}
