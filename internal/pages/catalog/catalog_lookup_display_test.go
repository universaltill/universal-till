package catalog

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// ut-docs#1430: same defect class as ut-docs#1178's tax-code fix (see
// tax_code_display_test.go) -- the catalog list's category column (there was
// none) and the item-edit category/brand fields both used to render the raw
// lookup id (a UUID in production) instead of the lookup's name. Both
// surfaces must show the name; neither may ever emit anything UUID-shaped.
func TestCatalogPage_CategoryBrandShowNameNotID(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	testsupport.SeedCategory(t, db, "8f14e45f-ceea-467e-a795-84f0e0c73c1b", "Snacks", true)
	testsupport.SeedBrand(t, db, "c4ca4238-a0b9-3382-8dcc-509a6f75849b", "Acme Foods", true)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "CRISPS", Name: "Crisps", BasePrice: 150, IsActive: true})
	if _, err := db.Exec(`UPDATE items SET category_id = ?, brand_id = ? WHERE id = 'itm1'`,
		"8f14e45f-ceea-467e-a795-84f0e0c73c1b", "c4ca4238-a0b9-3382-8dcc-509a6f75849b"); err != nil {
		t.Fatalf("assign category/brand: %v", err)
	}
	testsupport.SeedItem(t, db, testsupport.ItemSeed{
		ID: "itm2", SKU: "WATER", Name: "Still Water", BasePrice: 100, IsActive: true,
	}) // no category/brand at all

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}})

	req := httptest.NewRequest(http.MethodGet, "/catalog", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /catalog = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	if !strings.Contains(body, "Snacks") {
		t.Fatalf("expected category name %q to render somewhere on the page; got:\n%s", "Snacks", body)
	}
	if uuidLike.MatchString(stripDataAttrs(body)) {
		t.Fatalf("catalog page rendered a UUID-shaped string outside data-* attributes — a raw category/brand id leaked into visible content:\n%s", body)
	}

	// The item-edit category/brand controls must be <select>s populated
	// from the seeded lookups, not free-text/datalist inputs.
	if !strings.Contains(body, `<select name="categoryId"`) {
		t.Fatalf("expected the item-edit Category field to be a <select name=\"categoryId\">; got:\n%s", body)
	}
	if !strings.Contains(body, `<option value="8f14e45f-ceea-467e-a795-84f0e0c73c1b">Snacks</option>`) {
		t.Fatalf("expected the category <select> to offer the seeded category by name; got:\n%s", body)
	}
	if !strings.Contains(body, `<select name="brandId"`) {
		t.Fatalf("expected the item-edit Brand field to be a <select name=\"brandId\">; got:\n%s", body)
	}
	if !strings.Contains(body, `<option value="c4ca4238-a0b9-3382-8dcc-509a6f75849b">Acme Foods</option>`) {
		t.Fatalf("expected the brand <select> to offer the seeded brand by name; got:\n%s", body)
	}

	// The old datalist-based pickers are gone entirely.
	if strings.Contains(body, "categories-list") || strings.Contains(body, "brands-list") {
		t.Fatalf("expected the old <datalist> category/brand pickers to be removed; got:\n%s", body)
	}
}

// The affected item's card is also re-rendered standalone after every
// mutation (writeCatalogRowOOB, ut-docs#1363) — ut-docs#1951 dropped the
// Category column from the card grid entirely (product-owner direction:
// cards show name/price/photo only), so the card fragment no longer
// carries the category's name at all. What still matters, and is still
// worth pinning: the raw id must never leak into that fragment either, now
// that there's no display code left to carry the ut-docs#1430 fix forward.
func TestCatalogTablePartial_UpdateNeverLeaksRawCategoryID(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	testsupport.SeedCategory(t, db, "8f14e45f-ceea-467e-a795-84f0e0c73c1b", "Snacks", true)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "CRISPS", Name: "Crisps", BasePrice: 150, IsActive: true})
	if _, err := db.Exec(`UPDATE items SET category_id = ? WHERE id = 'itm1'`, "8f14e45f-ceea-467e-a795-84f0e0c73c1b"); err != nil {
		t.Fatalf("assign category: %v", err)
	}

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}})

	rec := postForm(t, mux, "/api/catalog/item/update",
		"id=itm1&name=Crisps&price=150&categoryId=8f14e45f-ceea-467e-a795-84f0e0c73c1b")
	if rec.Code != http.StatusOK {
		t.Fatalf("update = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="catalog-row-itm1"`) {
		t.Fatalf("expected the item's card fragment; got:\n%s", body)
	}
	if uuidLike.MatchString(stripDataAttrs(body)) {
		t.Fatalf("re-rendered card fragment leaked the raw category_id outside data-* attributes:\n%s", body)
	}
}
