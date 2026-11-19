package pages

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
)

// ut-docs#2654: a Turkish shop's imageless "Çay" row gets the tea icon.
func TestImport_PlaceholderMatchesShopLanguage(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	restore := httpx.SnapshotStateForTests()
	t.Cleanup(restore)
	i, err := config.NewI18nFS(fstest.MapFS{
		"en.json": {Data: []byte(`{"catalog.builtin_icon.tea":"Tea"}`)},
		"tr.json": {Data: []byte(`{"catalog.builtin_icon.tea":"Çay"}`)},
	}, "en")
	if err != nil {
		t.Fatal(err)
	}
	httpx.InitI18n(i, "tr")

	mux := http.NewServeMux()
	registerImport(mux, dp)
	csv := "Name,SKU,Price,Category\nÇay,T1,1.50,Drinks\nScone,B1,0.89,Counter\n"
	body, ct := multipartCSV(t, csv, map[string]string{"commit": "1"})
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("commit: code %d body %s", rec.Code, rec.Body.String())
	}
	var path string
	if err := dp.Db.QueryRow(`SELECT img.path FROM items i JOIN item_images img ON img.item_id = i.id AND img.role='thumbnail' WHERE i.sku='T1'`).Scan(&path); err != nil {
		t.Fatalf("thumbnail: %v", err)
	}
	if path != "/public/assets/category-icons/tea.svg" {
		t.Errorf("Çay thumbnail = %q, want tea.svg", path)
	}
}
