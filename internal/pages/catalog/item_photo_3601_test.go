package catalog

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/universaltill/universal-till/internal/testsupport"
)

// ut-docs#3601: a photo uploaded on the till keeps the item's stored icon.
func TestItemImageUpload_KeepsStoredIcon(t *testing.T) {
	mux, db := imageUploadTestDeps(t)
	testsupport.SeedTaxCode(t, db, "tax_std", "Standard", 2000)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "SKU1", Name: "Latte", BasePrice: 250, TaxCodeID: "tax_std", IsActive: true})
	if _, err := db.Exec(`UPDATE items SET icon = 'lucide:beer' WHERE id = 'itm1'`); err != nil {
		t.Fatal(err)
	}
	body, ct := multipartUpload(t, map[string]string{"item_id": "itm1"}, "photo.png", validPNG(t))
	req := httptest.NewRequest(http.MethodPost, "/api/catalog/item/image", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body.String())
	}
	var path, icon string
	if err := db.QueryRow(`SELECT (SELECT path FROM item_images WHERE item_id='itm1' AND role='thumbnail'), COALESCE(icon,'') FROM items WHERE id='itm1'`).Scan(&path, &icon); err != nil {
		t.Fatal(err)
	}
	if path != "/public/assets/items/itm1/thumb.png" || icon != "lucide:beer" {
		t.Fatalf("picture = %q / %q, want the photo and lucide:beer kept", path, icon)
	}
}
