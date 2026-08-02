package catalog

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// newCatalogMux is the standard harness for this file: fresh minimal-schema
// DB, fresh mux, default GBP/theme deps.
func newCatalogMux(t *testing.T) (*http.ServeMux, *sql.DB) {
	t.Helper()
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	t.Cleanup(func() { db.Close() })
	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}})
	return mux, db
}

func postForm(t *testing.T, mux *http.ServeMux, path, form string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func get(t *testing.T, mux *http.ServeMux, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// Every mutation endpoint registered without a method pattern must refuse
// non-POST — a GET reaching a mutation would let a prefetching browser or a
// crawler on the LAN admin UI mutate the catalog.
func TestCatalogMutationEndpoints_RejectGET(t *testing.T) {
	mux, _ := newCatalogMux(t)
	for _, path := range []string{
		"/api/catalog/item",
		"/api/catalog/item/update",
		"/api/catalog/item/deactivate",
		"/api/catalog/variant",
		"/api/catalog/variant/deactivate",
		"/api/catalog/modifier-group",
		"/api/catalog/modifier-option",
		"/api/catalog/barcode",
	} {
		rec := get(t, mux, path)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("GET %s: want 405, got %d", path, rec.Code)
		}
	}
}

func TestVariantOptions_ReturnsOnlyActiveVariants(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Shirt", BasePrice: 100, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v1", ItemID: "itm1", SKU: "S1-M", Name: "Medium", Price: 100, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v2", ItemID: "itm1", SKU: "S1-L", Name: "Large", Price: 120, IsActive: false})

	rec := get(t, mux, "/api/catalog/variant-options?item_id=itm1")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("bad JSON: %v: %s", err, rec.Body.String())
	}
	if len(envelope.Data) != 1 || envelope.Data[0].ID != "v1" || envelope.Data[0].Name != "Medium" {
		t.Fatalf("want only the active variant, got %+v", envelope.Data)
	}

	// No variants: an empty array, not null — the picker iterates it.
	rec2 := get(t, mux, "/api/catalog/variant-options?item_id=absent")
	if !strings.Contains(rec2.Body.String(), `"data":[]`) {
		t.Fatalf("want empty array for unknown item, got %s", rec2.Body.String())
	}
}

func TestVariantOptions_RepoErrorIs500(t *testing.T) {
	mux, db := newCatalogMux(t)
	if _, err := db.Exec(`DROP TABLE item_variants`); err != nil {
		t.Fatal(err)
	}
	rec := get(t, mux, "/api/catalog/variant-options?item_id=itm1")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestItemCost_ValidationAndClear(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Tea", BasePrice: 100, IsActive: true})

	if rec := postForm(t, mux, "/api/catalog/item-cost", "cost=1.00"); rec.Code != http.StatusBadRequest {
		t.Errorf("missing item: want 400, got %d", rec.Code)
	}
	for _, bad := range []string{"abc", "-1"} {
		if rec := postForm(t, mux, "/api/catalog/item-cost", "panelItem=itm1&cost="+bad); rec.Code != http.StatusBadRequest {
			t.Errorf("cost=%q: want 400, got %d", bad, rec.Code)
		}
	}

	// Set then clear: an empty cost stores 0 ("unknown"), the margin report's
	// no-data sentinel, and must succeed.
	if rec := postForm(t, mux, "/api/catalog/item-cost", "panelItem=itm1&cost=2.50"); rec.Code != http.StatusOK {
		t.Fatalf("set: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec := postForm(t, mux, "/api/catalog/item-cost", "panelItem=itm1&cost="); rec.Code != http.StatusOK {
		t.Fatalf("clear: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var minor int64
	if err := db.QueryRow(`SELECT cost_price FROM items WHERE id = 'itm1'`).Scan(&minor); err != nil {
		t.Fatal(err)
	}
	if minor != 0 {
		t.Fatalf("clearing the field must store 0, got %d", minor)
	}
}

func TestItemLeadTime_ValidationAndClear(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Tea", BasePrice: 100, IsActive: true})

	if rec := postForm(t, mux, "/api/catalog/item-lead-time", "leadTimeDays=5"); rec.Code != http.StatusBadRequest {
		t.Errorf("missing item: want 400, got %d", rec.Code)
	}
	for _, bad := range []string{"abc", "-1"} {
		if rec := postForm(t, mux, "/api/catalog/item-lead-time", "panelItem=itm1&leadTimeDays="+bad); rec.Code != http.StatusBadRequest {
			t.Errorf("leadTimeDays=%q: want 400, got %d", bad, rec.Code)
		}
	}

	// Set then clear: an empty value stores 0 (unset), and must succeed.
	if rec := postForm(t, mux, "/api/catalog/item-lead-time", "panelItem=itm1&leadTimeDays=10"); rec.Code != http.StatusOK {
		t.Fatalf("set: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec := get(t, mux, "/api/catalog/item-variants?item_id=itm1"); !strings.Contains(rec.Body.String(), `value="10"`) {
		t.Fatalf("expected the panel re-render to show the saved lead time, got %s", rec.Body.String())
	}
	if rec := postForm(t, mux, "/api/catalog/item-lead-time", "panelItem=itm1&leadTimeDays="); rec.Code != http.StatusOK {
		t.Fatalf("clear: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var days int
	if err := db.QueryRow(`SELECT lead_time_days FROM items WHERE id = 'itm1'`).Scan(&days); err != nil {
		t.Fatal(err)
	}
	if days != 0 {
		t.Fatalf("clearing the field must store 0, got %d", days)
	}
}

func TestItemLeadTime_RepoErrorIs500(t *testing.T) {
	mux, db := newCatalogMux(t)
	if _, err := db.Exec(`DROP TABLE items`); err != nil {
		t.Fatal(err)
	}
	if rec := postForm(t, mux, "/api/catalog/item-lead-time", "panelItem=itm1&leadTimeDays=5"); rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestItemCost_RepoErrorIs500(t *testing.T) {
	mux, db := newCatalogMux(t)
	if _, err := db.Exec(`DROP TABLE items`); err != nil {
		t.Fatal(err)
	}
	if rec := postForm(t, mux, "/api/catalog/item-cost", "panelItem=itm1&cost=1.00"); rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestItemCreate_InputValidation(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedCategory(t, db, "cat1", "Drinks", true)

	cases := []struct {
		name, form, wantErr string
	}{
		{"missing name", "price=100", "name and price required"},
		{"missing price", "name=Tea", "name and price required"},
		{"non-integer price", "name=Tea&price=1.99", "invalid price"},
		{"unknown category", "name=Tea&price=100&categoryId=nope", "invalid categories id"},
		{"unknown brand", "name=Tea&price=100&brandId=nope", "invalid brands id"},
		{"unknown tax code", "name=Tea&price=100&taxCode=nope", "invalid tax_codes id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := postForm(t, mux, "/api/catalog/item", tc.form)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("want 400, got %d: %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.wantErr) {
				t.Fatalf("want %q, got %s", tc.wantErr, rec.Body.String())
			}
		})
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM items`).Scan(&n)
	if n != 0 {
		t.Fatalf("no item should have been created by rejected input, found %d", n)
	}
}

// A create carrying a barcode that cannot attach (here: the item row itself
// was created inactive, and AddBarcode refuses inactive targets) reports the
// split outcome honestly: item created, attach failed, 400.
func TestItemCreate_BarcodeAttachFailureIsReported(t *testing.T) {
	mux, db := newCatalogMux(t)
	rec := postForm(t, mux, "/api/catalog/item", "name=Ghost&price=100&isActive=0&barcode=5000000000001")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "item created, but barcode attach failed") {
		t.Fatalf("want the split-outcome message, got %s", rec.Body.String())
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM items`).Scan(&n)
	if n != 1 {
		t.Fatalf("the item itself should still exist, found %d rows", n)
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM item_barcodes`).Scan(&n)
	if n != 0 {
		t.Fatalf("no barcode row should exist, found %d", n)
	}
}

// parseItemInput's checkbox/flag branches, observed through the DB row a
// real create produces.
func TestItemCreate_WeighedFlagAndFields(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedCategory(t, db, "cat1", "Produce", true)
	testsupport.SeedBrand(t, db, "b1", "Farm", true)
	testsupport.SeedTaxCode(t, db, "tax0", "Zero", 0)

	rec := postForm(t, mux, "/api/catalog/item",
		"name=Loose+Apples&price=89&sku=APL&isWeighed=on&unit=kg&description=Braeburn&categoryId=cat1&brandId=b1&taxCode=tax0")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var weighed int
	var unit, desc, cat, brand, tax string
	if err := db.QueryRow(`SELECT is_weighed, unit, description, category_id, brand_id, tax_code_id FROM items WHERE sku = 'APL'`).
		Scan(&weighed, &unit, &desc, &cat, &brand, &tax); err != nil {
		t.Fatal(err)
	}
	if weighed != 1 || unit != "kg" || desc != "Braeburn" || cat != "cat1" || brand != "b1" || tax != "tax0" {
		t.Fatalf("row wrong: weighed=%d unit=%q desc=%q cat=%q brand=%q tax=%q", weighed, unit, desc, cat, brand, tax)
	}
}

func TestItemUpdate_PersistsAndValidates(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Old Name", BasePrice: 100, IsActive: true})
	testsupport.SeedCategory(t, db, "cat1", "Drinks", true)

	rec := postForm(t, mux, "/api/catalog/item/update", "id=itm1&name=New+Name&price=250&sku=S1&categoryId=cat1&isActive=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var name string
	var price int64
	var cat string
	if err := db.QueryRow(`SELECT name, base_price, category_id FROM items WHERE id = 'itm1'`).Scan(&name, &price, &cat); err != nil {
		t.Fatal(err)
	}
	if name != "New Name" || price != 250 || cat != "cat1" {
		t.Fatalf("update not persisted: name=%q price=%d cat=%q", name, price, cat)
	}

	if rec := postForm(t, mux, "/api/catalog/item/update", "id=itm1&name=X&price=abc"); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid price: want 400, got %d", rec.Code)
	}
	if rec := postForm(t, mux, "/api/catalog/item/update", "id=itm1&name=X&price=100&categoryId=nope"); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown category: want 400, got %d", rec.Code)
	}
}

func TestItemDeactivate_RequiresID(t *testing.T) {
	mux, _ := newCatalogMux(t)
	if rec := postForm(t, mux, "/api/catalog/item/deactivate", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestVariantCreate_WithCostAndValidation(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Coffee", BasePrice: 300, IsActive: true})

	// Create through the vf-new form shape: price+cost arrive in minor units
	// (the template's utCurrency JS converts before submit).
	rec := postForm(t, mux, "/api/catalog/variant", "itemId=itm1&name=Large&sku=S1-L&price=350&costPrice=120&isActive=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("create: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var price, cost int64
	if err := db.QueryRow(`SELECT price, cost_price FROM item_variants WHERE sku = 'S1-L'`).Scan(&price, &cost); err != nil {
		t.Fatal(err)
	}
	if price != 350 || cost != 120 {
		t.Fatalf("variant stored wrong: price=%d cost=%d", price, cost)
	}

	cases := []struct{ name, form string }{
		{"missing itemId", "name=Large&price=350"},
		{"missing price", "itemId=itm1&name=Large"},
		{"non-integer price", "itemId=itm1&name=Large&price=3.50"},
		{"non-integer costPrice", "itemId=itm1&name=Large&price=350&costPrice=1.20"},
		{"duplicate sku", "itemId=itm1&name=Another&sku=S1-L&price=100"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if rec := postForm(t, mux, "/api/catalog/variant", tc.form); rec.Code != http.StatusBadRequest {
				t.Fatalf("want 400, got %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestVariantDeactivate_PanelAndValidation(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Coffee", BasePrice: 300, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v1", ItemID: "itm1", SKU: "S1-L", Name: "Large", Price: 350, IsActive: true})

	if rec := postForm(t, mux, "/api/catalog/variant/deactivate", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("missing id: want 400, got %d", rec.Code)
	}

	// With panelItem: deactivates AND answers with the panel + oob table.
	rec := postForm(t, mux, "/api/catalog/variant/deactivate", "id=v1&panelItem=itm1")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `id="catalog-table" hx-swap-oob="true"`) {
		t.Fatal("panel-scoped deactivate must carry the out-of-band items table")
	}
	var active int
	if err := db.QueryRow(`SELECT is_active FROM item_variants WHERE id = 'v1'`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 0 {
		t.Fatalf("variant still active: %d", active)
	}
}

func TestModifierGroup_UpdateRenameAndDeactivate(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Coffee", BasePrice: 300, IsActive: true})
	if _, err := db.Exec(`INSERT INTO item_modifier_groups (id, item_id, name, required, min_select, max_select, sort_order, is_active) VALUES ('g1','itm1','Extras',0,0,2,1,1)`); err != nil {
		t.Fatal(err)
	}

	// Rename + deactivate in one update (unchecked box: only the hidden 0).
	rec := postForm(t, mux, "/api/catalog/modifier-group", "panelItem=itm1&itemId=itm1&id=g1&name=Add-ons&minSelect=0&maxSelect=2&isActive=0")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var name string
	var active int
	if err := db.QueryRow(`SELECT name, is_active FROM item_modifier_groups WHERE id = 'g1'`).Scan(&name, &active); err != nil {
		t.Fatal(err)
	}
	if name != "Add-ons" || active != 0 {
		t.Fatalf("group update wrong: name=%q active=%d", name, active)
	}
}

func TestModifierGroup_Validation(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Coffee", BasePrice: 300, IsActive: true})

	if rec := postForm(t, mux, "/api/catalog/modifier-group", "itemId=itm1"); rec.Code != http.StatusBadRequest {
		t.Errorf("missing name: want 400, got %d", rec.Code)
	}
	if rec := postForm(t, mux, "/api/catalog/modifier-group", "name=Extras"); rec.Code != http.StatusBadRequest {
		t.Errorf("missing itemId: want 400, got %d", rec.Code)
	}

	// A missing/garbage maxSelect falls back to 1, never 0 — a max_select of
	// 0 would make every option unpickable at sale time.
	rec := postForm(t, mux, "/api/catalog/modifier-group", "panelItem=itm1&itemId=itm1&name=Size&isActive=1&maxSelect=garbage")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var maxSelect int
	if err := db.QueryRow(`SELECT max_select FROM item_modifier_groups WHERE name = 'Size'`).Scan(&maxSelect); err != nil {
		t.Fatal(err)
	}
	if maxSelect != 1 {
		t.Fatalf("want max_select fallback 1, got %d", maxSelect)
	}
}

func TestModifierOption_Validation(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Coffee", BasePrice: 300, IsActive: true})
	if _, err := db.Exec(`INSERT INTO item_modifier_groups (id, item_id, name, required, min_select, max_select, sort_order, is_active) VALUES ('g1','itm1','Extras',0,0,2,1,1)`); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ name, form string }{
		{"missing name", "groupId=g1&itemId=itm1"},
		{"missing groupId", "itemId=itm1&name=Shot"},
		{"missing itemId", "groupId=g1&name=Shot"},
		{"non-numeric priceDeltaMajor", "groupId=g1&itemId=itm1&name=Shot&priceDeltaMajor=abc"},
		{"negative priceDeltaMajor", "groupId=g1&itemId=itm1&name=Shot&priceDeltaMajor=-0.50"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if rec := postForm(t, mux, "/api/catalog/modifier-option", tc.form); rec.Code != http.StatusBadRequest {
				t.Fatalf("want 400, got %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestBarcodeAttach_VariantWinsOverItem(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Coffee", BasePrice: 300, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v1", ItemID: "itm1", SKU: "S1-L", Name: "Large", Price: 350, IsActive: true})

	// The form posts both (item row picked + variant chosen): the variant
	// must win and no item_barcodes row may appear.
	rec := postForm(t, mux, "/api/catalog/barcode", "barcode=5000001&itemId=itm1&variantId=v1&isPrimary=on")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var variantID string
	var primary int
	if err := db.QueryRow(`SELECT variant_id, is_primary FROM variant_barcodes WHERE barcode = '5000001'`).Scan(&variantID, &primary); err != nil {
		t.Fatalf("variant barcode not attached: %v", err)
	}
	if variantID != "v1" || primary != 1 {
		t.Fatalf("attached wrong: variant=%q primary=%d", variantID, primary)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM item_barcodes WHERE barcode = '5000001'`).Scan(&n)
	if n != 0 {
		t.Fatal("barcode must attach to exactly one target — item_barcodes row found too")
	}
}

func TestBarcodeAttach_Validation(t *testing.T) {
	mux, _ := newCatalogMux(t)
	if rec := postForm(t, mux, "/api/catalog/barcode", "itemId=itm1"); rec.Code != http.StatusBadRequest {
		t.Errorf("missing barcode: want 400, got %d", rec.Code)
	}
	// Unknown target item: the repo refuses, handler maps to 400.
	if rec := postForm(t, mux, "/api/catalog/barcode", "barcode=5000001&itemId=ghost"); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown item: want 400, got %d", rec.Code)
	}
}

func TestBarcodeDelete_Validation(t *testing.T) {
	mux, db := newCatalogMux(t)
	if rec := postForm(t, mux, "/api/catalog/barcode/delete", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("missing barcode: want 400, got %d", rec.Code)
	}
	// Repo failure surfaces as 400, not a silent 200.
	if _, err := db.Exec(`DROP TABLE variant_barcodes`); err != nil {
		t.Fatal(err)
	}
	if rec := postForm(t, mux, "/api/catalog/barcode/delete", "barcode=123456"); rec.Code != http.StatusBadRequest {
		t.Errorf("repo error: want 400, got %d", rec.Code)
	}
}

func TestCatalogPage_RepoErrorsAre500(t *testing.T) {
	t.Run("lookup tables missing", func(t *testing.T) {
		mux, db := newCatalogMux(t)
		if _, err := db.Exec(`DROP TABLE categories`); err != nil {
			t.Fatal(err)
		}
		if rec := get(t, mux, "/catalog"); rec.Code != http.StatusInternalServerError {
			t.Fatalf("want 500, got %d", rec.Code)
		}
	})
	t.Run("items table missing", func(t *testing.T) {
		mux, db := newCatalogMux(t)
		if _, err := db.Exec(`DROP TABLE items`); err != nil {
			t.Fatal(err)
		}
		if rec := get(t, mux, "/catalog"); rec.Code != http.StatusInternalServerError {
			t.Fatalf("want 500, got %d", rec.Code)
		}
	})
}

// An unknown item id renders the empty panel (pick-an-item state), not an
// error — the page loads before any row is chosen.
func TestVariantsPanel_UnknownItemRendersEmptyState(t *testing.T) {
	mux, _ := newCatalogMux(t)
	rec := get(t, mux, "/api/catalog/item-variants?item_id=ghost")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "catalog-detail-grid") {
		t.Fatal("unknown item must render the empty panel, not the detail grid")
	}
}

// bufResponseWriter backs the out-of-band table render; it must satisfy the
// http.ResponseWriter contract templates rely on: a non-nil, stable header
// map, buffered writes, and a status sink that never touches the real
// response (the outer handler already committed its own status).
func TestBufResponseWriter_Contract(t *testing.T) {
	var buf bytes.Buffer
	w := newBufResponseWriter(&buf)
	if w.Header() == nil {
		t.Fatal("Header() must not be nil")
	}
	w.Header().Set("X-Probe", "1")
	if got := w.Header().Get("X-Probe"); got != "1" {
		t.Fatalf("header map not stable: %q", got)
	}
	w.WriteHeader(http.StatusTeapot) // must be a no-op, not a panic
	if _, err := w.Write([]byte("fragment")); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "fragment" {
		t.Fatalf("writes must land in the buffer, got %q", buf.String())
	}
}
