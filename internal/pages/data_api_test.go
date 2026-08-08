package pages

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/settings"
)

func newDataAPITestDeps(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)

	cfg := &config.Config{Theme: "default", Locales: config.Locales{Currency: "GBP", TaxRate: 20}}
	pm, err := plugins.Init(t.Context(), cfg, db)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	dp := &common.Deps{
		Cfg:      cfg,
		Db:       db,
		State:    state,
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		Pm:       pm,
		Settings: settings.NewStore(db),
	}
	mux := http.NewServeMux()
	registerDataAPI(mux, dp)
	return mux, dp
}

func dataAPIJSONBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("expected valid JSON, got: %v\nbody: %s", err, rec.Body.String())
	}
	return out
}

// Every endpoint on this API is manager-gated and returns JSON, not an
// HTML redirect (unlike page handlers) -- without UT_AUTH=off and no
// signed-in manager in the request context, every one of them must
// refuse with 403 before touching the DB.
func TestDataAPI_AllEndpointsRequireManager(t *testing.T) {
	mux, _ := newDataAPITestDeps(t)
	cases := []struct {
		method, path string
	}{
		{http.MethodPost, "/api/data/reset-transactions"},
		{http.MethodGet, "/api/data/customers"},
		{http.MethodPost, "/api/data/customers/erase"},
		{http.MethodGet, "/api/data/obsolete-items"},
		{http.MethodPost, "/api/data/cleanup-catalog"},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, c.path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s: expected 403, got %d: %s", c.method, c.path, rec.Code, rec.Body.String())
		}
		body := dataAPIJSONBody(t, rec)
		if body["data"] != nil {
			t.Errorf("%s %s: expected data:null, got %+v", c.method, c.path, body)
		}
		if s, _ := body["error"].(string); s == "" {
			t.Errorf("%s %s: expected a non-empty error message, got %+v", c.method, c.path, body)
		}
	}
}

func TestResetTransactions_RequiresExactConfirmString(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newDataAPITestDeps(t)

	rec := postForm(mux, "/api/data/reset-transactions", nil, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 without confirm, got %d: %s", rec.Code, rec.Body.String())
	}
	req := httptest.NewRequest(http.MethodPost, "/api/data/reset-transactions", strings.NewReader("confirm=reset"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a lowercase/wrong confirm value, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestResetTransactions_ClearsSalesWhenConfirmed(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	if _, err := dp.Db.ExecContext(t.Context(), `INSERT INTO sales(id,receipt_no,status,sale_type,currency,subtotal,discount_total,tax_total,total,created_at) VALUES('s1','R001','completed','sale','GBP',100,0,0,100,datetime('now'))`); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/data/reset-transactions", strings.NewReader("confirm=RESET"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := dataAPIJSONBody(t, rec)
	if body["error"] != nil {
		t.Fatalf("expected error:null, got %+v", body)
	}
	data, _ := body["data"].(map[string]any)
	if data == nil || data["message"] != "cleared 1 sales and related records" {
		t.Fatalf("expected the exact cleared-count message under data, got %+v", body)
	}
	var count int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expected sales cleared, got %d rows left", count)
	}
	// The catalog is explicitly NOT part of what reset-transactions
	// touches (docs comment in data_api.go) -- confirm at the HTTP layer,
	// not just in the repo-level test, since this action is irreversible.
	var itm1 int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM items WHERE id='itm1'`).Scan(&itm1); err != nil {
		t.Fatal(err)
	}
	if itm1 != 1 {
		t.Fatalf("expected the catalog to survive a transaction reset")
	}
}

func TestGetCustomers_SearchesByQuery(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	if _, err := dp.Db.ExecContext(t.Context(), `INSERT INTO customers(id,name,phone,email) VALUES('cust1','Jane Doe','555','jane@example.com')`); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/data/customers?q=Jane", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Jane Doe") {
		t.Fatalf("expected Jane Doe in the search results, got: %s", rec.Body.String())
	}
	// Envelope: { "data": { "customers": [...] }, "error": null }
	// (universal-till/CLAUDE.md, ut-docs#387).
	body := dataAPIJSONBody(t, rec)
	if body["error"] != nil {
		t.Fatalf("expected error:null, got %+v", body)
	}
	data, _ := body["data"].(map[string]any)
	if data == nil || data["customers"] == nil {
		t.Fatalf("expected customers nested under data, got %+v", body)
	}
}

func TestEraseCustomer_RequiresIDAndReportsNotFound(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newDataAPITestDeps(t)

	rec := postForm(mux, "/api/data/customers/erase", nil, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 without an id, got %d: %s", rec.Code, rec.Body.String())
	}

	req := httptest.NewRequest(http.MethodPost, "/api/data/customers/erase", strings.NewReader("id=does-not-exist"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for an unknown customer, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestEraseCustomer_ErasesRealCustomer(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	if _, err := dp.Db.ExecContext(t.Context(), `INSERT INTO customers(id,name) VALUES('cust1','Jane Doe')`); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/data/customers/erase", strings.NewReader("id=cust1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var count int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM customers WHERE id='cust1'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expected the customer erased, still found %d rows", count)
	}
}

func TestGetObsoleteItems_ListsInactiveNeverSoldItems(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	if _, err := dp.Db.ExecContext(t.Context(), `INSERT INTO items(id,sku,name,base_price,is_active) VALUES('obs1','OLD','Old Discontinued Product',100,0)`); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/data/obsolete-items", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Old Discontinued Product") {
		t.Fatalf("expected the obsolete item listed, got: %s", rec.Body.String())
	}
	// itm1 (seedForPages) is active with stock, and must NOT show up.
	if strings.Contains(rec.Body.String(), "Apple") {
		t.Fatalf("expected the active seeded item to be excluded, got: %s", rec.Body.String())
	}
	// Envelope: { "data": { "items": [...] }, "error": null }
	// (universal-till/CLAUDE.md, ut-docs#387).
	body := dataAPIJSONBody(t, rec)
	if body["error"] != nil {
		t.Fatalf("expected error:null, got %+v", body)
	}
	data, _ := body["data"].(map[string]any)
	if data == nil || data["items"] == nil {
		t.Fatalf("expected items nested under data, got %+v", body)
	}
}

func TestCleanupCatalog_RequiresExactConfirmString(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newDataAPITestDeps(t)
	rec := postForm(mux, "/api/data/cleanup-catalog", nil, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 without confirm, got %d: %s", rec.Code, rec.Body.String())
	}
	req := httptest.NewRequest(http.MethodPost, "/api/data/cleanup-catalog", strings.NewReader("confirm=cleanup"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a lowercase/wrong confirm value, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCleanupCatalog_RemovesObsoleteItemsWhenConfirmed(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	if _, err := dp.Db.ExecContext(t.Context(), `INSERT INTO items(id,sku,name,base_price,is_active) VALUES('obs1','OLD','Old Discontinued Product',100,0)`); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/data/cleanup-catalog", strings.NewReader("confirm=CLEANUP"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var count int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM items WHERE id='obs1'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expected the obsolete item removed, still found %d rows", count)
	}
	// itm1 (seedForPages, active) must survive a catalog cleanup.
	var itm1 int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM items WHERE id='itm1'`).Scan(&itm1); err != nil {
		t.Fatal(err)
	}
	if itm1 != 1 {
		t.Fatalf("expected the active seeded item to survive cleanup")
	}
}
