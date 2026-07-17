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

// The per-item variants editor (Farshid: "when I click an item I should see
// all the variants and edit them, each with a barcode"): the panel renders
// every variant with sku/price/active state and all barcodes; panel
// mutations answer with the panel PLUS the items table as an out-of-band
// swap; unchecking Active really deactivates (checkbox + hidden-0 pair).
func TestCatalogVariantsPanel(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "COLA", Name: "Cola", BasePrice: 120, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v1", ItemID: "itm1", SKU: "COLA-330", Name: "330ml", Price: 100, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v2", ItemID: "itm1", SKU: "COLA-1L", Name: "1 Litre", Price: 250, IsActive: true})
	testsupport.SeedBarcode(t, db, "5000001", "itm1", true)
	testsupport.SeedVariantBarcode(t, db, "5000330", "v1", true)

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}})

	// Panel render: both variants, their SKUs, the item barcode, the variant
	// barcode, and an add-variant form.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/catalog/item-variants?item_id=itm1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("panel: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"Cola", "330ml", "1 Litre", "COLA-330", "5000001", "5000330", "/api/catalog/variant", "/api/catalog/barcode/delete",
		// The redesigned layout: info + variants grid, rows wired to hidden
		// forms via the form="" attribute (htmx serializes form.elements).
		"catalog-detail-grid", "vg-cols", `form="vf-v1"`, `id="vf-v1"`, `id="vf-new"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("panel missing %q in:\n%s", want, body)
		}
	}

	// Edit via the panel: rename + uncheck Active (checkbox absent, hidden 0
	// present). Response = panel + out-of-band items table.
	form := "panelItem=itm1&id=v1&itemId=itm1&name=330ml+Can&sku=COLA-330&price=110&isActive=0"
	req := httptest.NewRequest(http.MethodPost, "/api/catalog/variant", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusOK {
		t.Fatalf("variant save: want 200, got %d: %s", rec2.Code, rec2.Body.String())
	}
	if !strings.Contains(rec2.Body.String(), `id="catalog-table" hx-swap-oob="true"`) {
		t.Fatal("panel mutation response missing the out-of-band items table")
	}
	var name string
	var active int
	if err := db.QueryRow(`SELECT name, is_active FROM item_variants WHERE id = 'v1'`).Scan(&name, &active); err != nil {
		t.Fatalf("read variant: %v", err)
	}
	if name != "330ml Can" || active != 0 {
		t.Fatalf("variant not updated: name=%q active=%d (unchecked box must deactivate)", name, active)
	}

	// Barcode delete via the panel.
	req3 := httptest.NewRequest(http.MethodPost, "/api/catalog/barcode/delete", strings.NewReader("barcode=5000330&panelItem=itm1"))
	req3.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec3 := httptest.NewRecorder()
	mux.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Fatalf("barcode delete: want 200, got %d: %s", rec3.Code, rec3.Body.String())
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM variant_barcodes WHERE barcode = '5000330'`).Scan(&n)
	if n != 0 {
		t.Fatal("variant barcode not deleted")
	}
}
