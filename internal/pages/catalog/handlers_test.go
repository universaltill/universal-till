package catalog

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/testsupport"
)

func setupCatalogPageDB(t *testing.T) *sql.DB {
	t.Helper()
	return testsupport.NewCatalogTestDB(t)
}

func TestCatalogPage_FiltersInactive(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "active1", SKU: "A1", Name: "Active Item", BasePrice: 100, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "inactive1", SKU: "I1", Name: "Inactive Item", BasePrice: 200, IsActive: false})
	testsupport.SeedCategory(t, db, "cat1", "Cat 1", true)
	testsupport.SeedBrand(t, db, "brand1", "Brand 1", true)
	testsupport.SeedTaxCode(t, db, "tax_std", "Standard", 2000)

	mux := http.NewServeMux()
	dp := &common.Deps{
		Db:    db,
		State: common.RuntimeState{Theme: "default"},
		Menu:  []common.MenuItem{},
	}
	Register(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/catalog", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Active Item") {
		t.Fatalf("expected active item in response")
	}
	if strings.Contains(body, "Inactive Item") {
		t.Fatalf("expected inactive item to be filtered out")
	}
}

func TestCatalogCreateAndDeactivate(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	testsupport.SeedTaxCode(t, db, "tax_std", "Standard", 2000)
	mux := http.NewServeMux()
	dp := &common.Deps{
		Db:    db,
		State: common.RuntimeState{Theme: "default"},
		Menu:  []common.MenuItem{},
	}
	Register(mux, dp)

	// create item
	form := strings.NewReader("name=Test+Item&price=123&sku=T1&taxCode=tax_std&isActive=1")
	req := httptest.NewRequest(http.MethodPost, "/api/catalog/item", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on create, got %d: %s", rec.Code, rec.Body.String())
	}
	// deactivate
	deact := strings.NewReader("id=" + extractItemID(t, db))
	req = httptest.NewRequest(http.MethodPost, "/api/catalog/item/deactivate", deact)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on deactivate, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCatalogLookupRejectsInvalidBarcode(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}})

	for _, q := range []string{"", "abc", "12345", "123456789012345"} {
		req := httptest.NewRequest(http.MethodGet, "/api/catalog/lookup?barcode="+q, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("barcode %q: expected 400, got %d: %s", q, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"error"`) {
			t.Errorf("barcode %q: expected JSON error envelope, got %s", q, rec.Body.String())
		}
	}
}

func TestCatalogCreateWithBarcodeAttachesPrimary(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}})

	form := strings.NewReader("name=Cola&price=150&isActive=1&barcode=5449000000996")
	req := httptest.NewRequest(http.MethodPost, "/api/catalog/item", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on create, got %d: %s", rec.Code, rec.Body.String())
	}
	var itemID string
	var isPrimary int
	err := db.QueryRow(`SELECT item_id, is_primary FROM item_barcodes WHERE barcode = '5449000000996'`).
		Scan(&itemID, &isPrimary)
	if err != nil {
		t.Fatalf("barcode not attached: %v", err)
	}
	if itemID != extractItemID(t, db) || isPrimary != 1 {
		t.Errorf("barcode attached wrong: item=%s primary=%d", itemID, isPrimary)
	}
}

func extractItemID(t *testing.T, db *sql.DB) string {
	t.Helper()
	var id string
	if err := db.QueryRow(`SELECT id FROM items LIMIT 1`).Scan(&id); err != nil {
		t.Fatalf("extract id: %v", err)
	}
	return id
}

// chdirToRepoRoot makes template paths resolve correctly in tests.
func chdirToRepoRoot(t *testing.T) {
	t.Helper()
	_, filename, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", "..", ".."))
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir: %v", err)
	}
}
