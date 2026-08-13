package pages

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/db"
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
		{http.MethodGet, "/api/data/reset-archives"},
		{http.MethodPost, "/api/data/reset-archives/some-batch/restore"},
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
	msg, _ := data["message"].(string)
	// ADR-0042: the records are archived, not destroyed — the response says
	// so (and must NOT claim anything was permanently cleared/deleted).
	if data == nil || !strings.HasPrefix(msg, "archived 1 sales") {
		t.Fatalf("expected an archived-count message under data, got %+v", body)
	}
	var count int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expected sales cleared, got %d rows left", count)
	}
	// ...and moved into the archive, tagged with a batch.
	var archived, batches int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM sales_archive`).Scan(&archived); err != nil {
		t.Fatal(err)
	}
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM reset_batches`).Scan(&batches); err != nil {
		t.Fatal(err)
	}
	if archived != 1 || batches != 1 {
		t.Fatalf("expected 1 archived sale in 1 batch, got %d/%d", archived, batches)
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

// ADR-0042: a reset moves everything into an archive batch; the batch is
// listable and restorable (whole-batch, typed RESTORE confirmation) as long
// as the till has not traded since.
func TestResetArchives_ListAndRestoreRoundTrip(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	if _, err := dp.Db.ExecContext(t.Context(), `INSERT INTO sales(id,receipt_no,status,sale_type,currency,subtotal,discount_total,tax_total,total,created_at) VALUES('s1','R001','completed','sale','GBP',100,0,0,100,datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	rec := postForm(mux, "/api/data/reset-transactions", url.Values{"confirm": {"RESET"}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("reset: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// List: { data: { batches: [ {id, created_at, actor_id, sales_count} ] } }
	req := httptest.NewRequest(http.MethodGet, "/api/data/reset-archives", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := dataAPIJSONBody(t, rec)
	if body["error"] != nil {
		t.Fatalf("list: expected error:null, got %+v", body)
	}
	data, _ := body["data"].(map[string]any)
	batches, _ := data["batches"].([]any)
	if len(batches) != 1 {
		t.Fatalf("list: expected 1 batch, got %+v", body)
	}
	batch, _ := batches[0].(map[string]any)
	id, _ := batch["id"].(string)
	if id == "" {
		t.Fatalf("list: batch must carry a snake_case id, got %+v", batch)
	}
	if sc, _ := batch["sales_count"].(float64); sc != 1 {
		t.Fatalf("list: sales_count = %v, want 1", batch["sales_count"])
	}
	if created, _ := batch["created_at"].(string); created == "" || !strings.Contains(created, "T") {
		t.Fatalf("list: created_at must be ISO-8601, got %+v", batch)
	}

	// Restore requires the typed confirmation.
	rec = postForm(mux, "/api/data/reset-archives/"+id+"/restore", url.Values{"confirm": {"restore"}}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("restore with wrong confirm: expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	rec = postForm(mux, "/api/data/reset-archives/"+id+"/restore", url.Values{"confirm": {"RESTORE"}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("restore: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var live, archived, remaining int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM sales_archive`).Scan(&archived); err != nil {
		t.Fatal(err)
	}
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM reset_batches`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if live != 1 || archived != 0 || remaining != 0 {
		t.Fatalf("after restore: live=%d archived=%d batches=%d, want 1/0/0", live, archived, remaining)
	}
}

func TestResetArchivesRestore_NotFoundAndConflict(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)

	// Unknown batch → 404, not a 500/panic.
	rec := postForm(mux, "/api/data/reset-archives/no-such-batch/restore", url.Values{"confirm": {"RESTORE"}}, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown batch: expected 404, got %d: %s", rec.Code, rec.Body.String())
	}

	// Reset, then trade: restore must refuse with 409 Conflict (ADR-0042 §2).
	if _, err := dp.Db.ExecContext(t.Context(), `INSERT INTO sales(id,receipt_no,status,sale_type,currency,subtotal,discount_total,tax_total,total,created_at) VALUES('s1','R001','completed','sale','GBP',100,0,0,100,datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	rec = postForm(mux, "/api/data/reset-transactions", url.Values{"confirm": {"RESET"}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("reset: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var batchID string
	if err := dp.Db.QueryRow(`SELECT id FROM reset_batches`).Scan(&batchID); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(t.Context(), `INSERT INTO sales(id,receipt_no,status,sale_type,currency,subtotal,discount_total,tax_total,total,created_at) VALUES('post1','R001','completed','sale','GBP',50,0,0,50,datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	rec = postForm(mux, "/api/data/reset-archives/"+batchID+"/restore", url.Values{"confirm": {"RESTORE"}}, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("restore after trading: expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	body := dataAPIJSONBody(t, rec)
	if s, _ := body["error"].(string); s == "" {
		t.Fatalf("409 must carry a clear error message, got %+v", body)
	}
	// Refusal touches nothing.
	var live, archived int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM sales_archive`).Scan(&archived); err != nil {
		t.Fatal(err)
	}
	if live != 1 || archived != 1 {
		t.Fatalf("after refused restore: live=%d archived=%d, want 1/1", live, archived)
	}
}

// newRealDBDataAPIDeps wires registerDataAPI over a REAL migrated database
// (internal/db.Open), not the seedForPages fixture: that fixture's
// hand-rolled sale_lines table (ui_smoke_test.go) declares only the sale_id
// FK, not item_id -> items — the exact fixture-drift trap the tester skill
// warns about (identical to the demo_seed_opt_in_test.go precedent this
// mirrors). TestResetArchivesRestore_ReferencesRemovedItem below needs the
// REAL FK to be enforced to mean anything.
func newRealDBDataAPIDeps(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	dbo, err := db.Open(filepath.Join(t.TempDir(), "reset-archive-real.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { dbo.Close() })
	dp := &common.Deps{
		Db:       dbo.DB,
		Settings: settings.NewStore(dbo.DB),
		Cfg:      &config.Config{Theme: "default", Locales: config.Locales{Currency: "GBP", TaxRate: 20}},
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
	}
	mux := http.NewServeMux()
	registerDataAPI(mux, dp)
	return mux, dp
}

// Independent review, ut-docs#187: with sale_lines emptied by reset, an
// item-cleanup action (catalog cleanup / "Remove sample data") sees no live
// reference and deletes the item an archived batch still points to. Restore
// must then refuse with 422, not a raw 500 carrying a SQL error string.
func TestResetArchivesRestore_ReferencesRemovedItem(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newRealDBDataAPIDeps(t)
	if _, err := dp.Db.ExecContext(t.Context(), `INSERT INTO items(id,sku,name,base_price,is_active) VALUES('itm-reset187','SKU-RESET187','Widget',100,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(t.Context(), `INSERT INTO sales(id,receipt_no,status,sale_type,currency,subtotal,discount_total,tax_total,total,created_at) VALUES('s1','R001','completed','sale','GBP',100,0,0,100,datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(t.Context(), `INSERT INTO sale_lines(id,sale_id,line_no,item_id,name_snapshot,quantity,unit_price,tax_rate_bp,tax_amount,total_before_tax,total_after_tax) VALUES('l1','s1',1,'itm-reset187','Widget',1,100,0,0,100,100)`); err != nil {
		t.Fatal(err)
	}
	rec := postForm(mux, "/api/data/reset-transactions", url.Values{"confirm": {"RESET"}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("reset: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var batchID string
	if err := dp.Db.QueryRow(`SELECT id FROM reset_batches`).Scan(&batchID); err != nil {
		t.Fatal(err)
	}
	// sale_lines is empty now, so the item looks unreferenced and safe to
	// remove — exactly what "Remove sample data"/catalog cleanup do.
	if _, err := dp.Db.ExecContext(t.Context(), `DELETE FROM items WHERE id='itm-reset187'`); err != nil {
		t.Fatal(err)
	}

	rec = postForm(mux, "/api/data/reset-archives/"+batchID+"/restore", url.Values{"confirm": {"RESTORE"}}, nil)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("restore after item removed: expected 422, got %d: %s", rec.Code, rec.Body.String())
	}
	body := dataAPIJSONBody(t, rec)
	if s, _ := body["error"].(string); s == "" {
		t.Fatalf("422 must carry a clear error message, got %+v", body)
	}
	// Refusal touches nothing: live tables stay empty, archive intact.
	var live, archived int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM sales_archive`).Scan(&archived); err != nil {
		t.Fatal(err)
	}
	if live != 0 || archived != 1 {
		t.Fatalf("after refused restore: live=%d archived=%d, want 0/1", live, archived)
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
