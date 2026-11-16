package catalog

// ut-docs#4846 (owner decision C): picking a built-in image on an item that
// has an uploaded photo stores it as the photo's fallback (items.icon) and
// keeps the photo row and thumb.png; an item without a photo behaves as
// before; "No image" still clears both.

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/iconid"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/testsupport"
	"github.com/universaltill/universal-till/internal/ui"
)

const photoPath4846 = "/public/assets/items/itm1/thumb.png"

// seedPhotoItem4846 seeds itm1 with a real PNG at items/itm1/thumb.png and
// its thumbnail row; returns the file path.
func seedPhotoItem4846(t *testing.T, db *sql.DB) string {
	t.Helper()
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "SKU1", Name: "Item", BasePrice: 100, IsActive: true})
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(paths.Data("public", "assets", "items", "itm1"), "thumb.png")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO item_images (id, item_id, path, role) VALUES ('img1','itm1',?,'thumbnail')`, photoPath4846); err != nil {
		t.Fatal(err)
	}
	return file
}

func pictureOf4846(t *testing.T, db *sql.DB) (thumb, icon string) {
	t.Helper()
	repo := data.NewCatalogRepo(db)
	thumb, _, err := repo.ItemThumbnailPath(t.Context(), "itm1")
	if err != nil {
		t.Fatal(err)
	}
	icon, err = repo.ItemIcon(t.Context(), "itm1")
	if err != nil {
		t.Fatal(err)
	}
	return thumb, icon
}

func TestChooseIcon_PhotoItemKeepsPhotoAndStoresFallback_4846(t *testing.T) {
	mux, db := iconPickerFileTestDeps(t)
	file := seedPhotoItem4846(t, db)

	rec := postForm(t, mux, "/api/catalog/item/icon", "item_id=itm1&icon=beer")
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d body %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("HX-Trigger"); got != "item-icon-photo-kept" {
		t.Errorf("HX-Trigger = %q, want item-icon-photo-kept", got)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("the photo file must be kept: %v", err)
	}
	thumb, icon := pictureOf4846(t, db)
	if thumb != photoPath4846 || icon != "lucide:beer" {
		t.Fatalf("thumb=%q icon=%q, want the photo kept and lucide:beer stored", thumb, icon)
	}
	// Everything resolving the picture still shows the photo...
	if got := ui.ItemThumb(thumb, icon); got != photoPath4846 {
		t.Errorf("ItemThumb = %q, want the photo", got)
	}
	if !strings.Contains(rec.Body.String(), "items/itm1/thumb.png") {
		t.Errorf("row fragment should still show the photo:\n%s", rec.Body.String())
	}
	// ...and once the photo is removed the picked icon shows (AC4).
	if err := data.NewCatalogRepo(db).ClearItemPhoto(t.Context(), "itm1"); err != nil {
		t.Fatal(err)
	}
	thumb, icon = pictureOf4846(t, db)
	if got := iconid.Picture(thumb, icon); got != "/public/assets/category-icons/beer.svg" {
		t.Errorf("Picture after photo removed = %q, want the beer tile", got)
	}
}

func TestChooseIcon_GenericOnPhotoItemWritesNothing_4846(t *testing.T) {
	mux, db := iconPickerFileTestDeps(t)
	file := seedPhotoItem4846(t, db)
	if _, err := db.Exec(`UPDATE items SET icon='lucide:coffee' WHERE id='itm1'`); err != nil {
		t.Fatal(err)
	}
	rec := postForm(t, mux, "/api/catalog/item/icon", "item_id=itm1&icon=generic")
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d body %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("HX-Trigger") != "item-icon-photo-kept" {
		t.Errorf("HX-Trigger = %q", rec.Header().Get("HX-Trigger"))
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("photo file removed: %v", err)
	}
	if thumb, icon := pictureOf4846(t, db); thumb != photoPath4846 || icon != "lucide:coffee" {
		t.Fatalf("thumb=%q icon=%q, want unchanged", thumb, icon)
	}
}

func TestChooseIcon_NonPhotoItemStillShowsPickedIcon_4846(t *testing.T) {
	mux, db := iconPickerFileTestDeps(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "SKU1", Name: "Item", BasePrice: 100, IsActive: true})
	if _, err := db.Exec(`INSERT INTO item_images (id, item_id, path, role) VALUES ('img1','itm1','/public/assets/category-icons/coffee.svg','thumbnail')`); err != nil {
		t.Fatal(err)
	}
	rec := postForm(t, mux, "/api/catalog/item/icon", "item_id=itm1&icon=beer")
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d body %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("HX-Trigger") != "" {
		t.Errorf("HX-Trigger = %q, want none for a non-photo item", rec.Header().Get("HX-Trigger"))
	}
	if thumb, icon := pictureOf4846(t, db); thumb != "/public/assets/category-icons/beer.svg" || icon != "" {
		t.Fatalf("thumb=%q icon=%q, want the beer tile and no icon", thumb, icon)
	}
}

func TestChooseIcon_NoImageClearsPhotoAndIcon_4846(t *testing.T) {
	mux, db := iconPickerFileTestDeps(t)
	file := seedPhotoItem4846(t, db)
	if _, err := db.Exec(`UPDATE items SET icon='lucide:beer' WHERE id='itm1'`); err != nil {
		t.Fatal(err)
	}
	if rec := postForm(t, mux, "/api/catalog/item/icon", "item_id=itm1&icon=none"); rec.Code != http.StatusOK {
		t.Fatalf("code %d", rec.Code)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Errorf("photo file should be removed, stat err = %v", err)
	}
	if thumb, icon := pictureOf4846(t, db); thumb != "" || icon != "" {
		t.Fatalf("thumb=%q icon=%q, want both cleared", thumb, icon)
	}
}

func TestIconState_ReportsFallbackKey_4846(t *testing.T) {
	mux, db := iconPickerFileTestDeps(t)
	seedPhotoItem4846(t, db)
	decode := func() map[string]any {
		rec := get(t, mux, "/api/catalog/item/icon-state?item_id=itm1")
		var body struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v %s", err, rec.Body.String())
		}
		return body.Data
	}
	if d := decode(); d["fallback_key"] != "" || d["is_custom"] != true {
		t.Fatalf("no icon yet: %v", d)
	}
	if _, err := db.Exec(`UPDATE items SET icon='lucide:beer' WHERE id='itm1'`); err != nil {
		t.Fatal(err)
	}
	if d := decode(); d["fallback_key"] != "beer" || d["selected_key"] != "" || d["is_custom"] != true {
		t.Fatalf("photo + icon: %v", d)
	}
	if _, err := db.Exec(`UPDATE items SET icon='lucide:not-in-library' WHERE id='itm1'`); err != nil {
		t.Fatal(err)
	}
	if d := decode(); d["fallback_key"] != "" {
		t.Fatalf("unknown id: %v", d)
	}
}
