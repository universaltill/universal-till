package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/settings"
)

func newInventoryAPITestDeps(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)

	cfg := &config.Config{
		Theme:   "default",
		Locales: config.Locales{Currency: "GBP", TaxRate: 20},
		Marketplace: config.MarketplaceConfig{
			EndpointURL: "http://localhost:8081",
		},
	}
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
		AuthSvc:  auth.NewService(db),
	}
	mux := http.NewServeMux()
	registerInventoryAPI(mux, dp)
	return mux, dp
}

func postInvForm(t *testing.T, mux *http.ServeMux, path, form, accept string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func postInvJSON(t *testing.T, mux *http.ServeMux, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestCreateStockReceipt_JSONAndHTML(t *testing.T) {
	mux, _ := newInventoryAPITestDeps(t)

	// Positive: JSON accept returns a JSON body with success=true.
	rec := postInvJSON(t, mux, "/api/inventory/receipt", `{"type":"receive","item_id":"itm1","location_id":"loc_main","quantity":5,"reason":"delivery"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"success":true`) {
		t.Fatalf("expected JSON success response, got %s", rec.Body.String())
	}

	// Positive: form + HTML accept, HX-Trigger set for the table refresh.
	rec = postInvForm(t, mux, "/api/inventory/receipt", "type=adjust&item_id=itm1&location_id=loc_main&quantity=-2&reason=breakage", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("HX-Trigger") != "stock-updated" {
		t.Fatal("expected HX-Trigger: stock-updated on a successful HTML response")
	}
}

func TestCreateStockReceipt_InvalidJSON(t *testing.T) {
	mux, _ := newInventoryAPITestDeps(t)
	rec := postInvJSON(t, mux, "/api/inventory/receipt", `{not valid json`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid JSON, got %d", rec.Code)
	}
}

func TestCreateStockReceipt_ValidationErrors(t *testing.T) {
	mux, _ := newInventoryAPITestDeps(t)
	cases := []struct {
		name, form string
	}{
		{"bad type", "type=bogus&item_id=itm1&location_id=loc_main&quantity=5"},
		{"missing location", "type=receive&item_id=itm1&quantity=5"},
		{"missing item and variant", "type=receive&location_id=loc_main&quantity=5"},
		{"zero quantity", "type=receive&item_id=itm1&location_id=loc_main&quantity=0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := postInvForm(t, mux, "/api/inventory/receipt", c.form, "application/json")
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestCreateStockReceipt_RequiresAuthenticatedActor(t *testing.T) {
	mux, _ := newInventoryAPITestDeps(t)
	req := httptest.NewRequest(http.MethodPost, "/api/inventory/receipt",
		strings.NewReader("type=receive&item_id=itm1&location_id=loc_main&quantity=5"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// auth.WithUser with a blank ID simulates a context carrying no real
	// operator — getSessionUserID/auth.UserID falls back to "system" only
	// when there is NO user in context at all; forcing an explicit
	// empty-ID user exercises the handler's own belt-and-braces check.
	req = auth.WithUser(req, auth.User{ID: ""})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for an empty actor id, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateStockReceipt_MalformedFormBody(t *testing.T) {
	mux, _ := newInventoryAPITestDeps(t)
	req := httptest.NewRequest(http.MethodPost, "/api/inventory/receipt", strings.NewReader("%zz"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unparseable form body, got %d", rec.Code)
	}
}

func TestCreateNegativeInventoryOverride_AdminNeedsNoPIN(t *testing.T) {
	mux, _ := newInventoryAPITestDeps(t)
	req := httptest.NewRequest(http.MethodPost, "/api/inventory/override",
		strings.NewReader("reason=stock+count+correction&item_id=itm1&location_id=loc_main&qty_before=50"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = auth.WithUser(req, auth.User{ID: "user1", Role: "admin"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for an admin override with no PIN, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("HX-Trigger") != "stock-updated" {
		t.Fatal("expected HX-Trigger: stock-updated on a successful override")
	}
}

func TestCreateNegativeInventoryOverride_CashierRequiresManagerPIN(t *testing.T) {
	mux, dp := newInventoryAPITestDeps(t)
	ctx := context.Background()

	if _, err := dp.Db.ExecContext(ctx,
		`INSERT INTO users(id,username,display_name,pin_hash,role,created_at) VALUES('cashier1','cashier1','Cashier One','','cashier',datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPIN("482913")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx,
		`INSERT INTO users(id,username,display_name,pin_hash,role,created_at) VALUES('mgr1','mgr1','Manager One',?,'manager',datetime('now'))`, hash); err != nil {
		t.Fatal(err)
	}

	makeReq := func(form string, actor string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/inventory/override", strings.NewReader(form))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req = auth.WithUser(req, auth.User{ID: actor, Role: "cashier"})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	// No PIN at all: forbidden.
	rec := makeReq("reason=correction&item_id=itm1&location_id=loc_main&qty_before=50", "cashier1")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 with no manager PIN, got %d: %s", rec.Code, rec.Body.String())
	}

	// Wrong PIN: forbidden with a distinct message.
	rec = makeReq("reason=correction&item_id=itm1&location_id=loc_main&qty_before=50&manager_pin=000000", "cashier1")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 with a wrong PIN, got %d: %s", rec.Code, rec.Body.String())
	}

	// Correct manager PIN: succeeds, and the MANAGER (not the cashier who
	// submitted the request) becomes the audit actor — the whole point of
	// the PIN-approval flow (docs: architecture/pos-auth.md).
	rec = makeReq("reason=correction&item_id=itm1&location_id=loc_main&qty_before=50&manager_pin=482913", "cashier1")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with a correct manager PIN, got %d: %s", rec.Code, rec.Body.String())
	}
	overrideID := strings.TrimSuffix(strings.TrimPrefix(rec.Body.String(), "<div class='success'>Override recorded: "), "</div>")
	var actorID string
	if err := dp.Db.QueryRowContext(ctx, `SELECT actor_id FROM audit_log WHERE id = ?`, overrideID).Scan(&actorID); err != nil {
		t.Fatalf("query audit_log for override %q: %v", overrideID, err)
	}
	if actorID != "mgr1" {
		t.Fatalf("expected the approving manager (mgr1) to be the audit actor, got %q", actorID)
	}
}

func TestCreateNegativeInventoryOverride_ValidationErrors(t *testing.T) {
	mux, _ := newInventoryAPITestDeps(t)
	makeReq := func(form string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/inventory/override", strings.NewReader(form))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req = auth.WithUser(req, auth.User{ID: "user1", Role: "admin"})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	cases := []struct{ name, form string }{
		{"missing reason", "item_id=itm1&location_id=loc_main&qty_before=50"},
		{"missing location", "reason=x&item_id=itm1&qty_before=50"},
		{"missing item and variant", "reason=x&location_id=loc_main&qty_before=50"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := makeReq(c.form)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
			}
		})
	}

	// Unknown user id: forbidden with "user not found".
	req := httptest.NewRequest(http.MethodPost, "/api/inventory/override",
		strings.NewReader("reason=x&item_id=itm1&location_id=loc_main&qty_before=50"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = auth.WithUser(req, auth.User{ID: "does-not-exist"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for an unknown user, got %d: %s", rec.Code, rec.Body.String())
	}

	// Empty actor id: unauthorized.
	req = httptest.NewRequest(http.MethodPost, "/api/inventory/override",
		strings.NewReader("reason=x&item_id=itm1&location_id=loc_main&qty_before=50"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = auth.WithUser(req, auth.User{ID: ""})
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for an empty actor id, got %d: %s", rec.Code, rec.Body.String())
	}
}

func seedCompletedSaleForReturn(t *testing.T, dp *common.Deps) (saleID, receiptNo, lineID string) {
	t.Helper()
	ctx := context.Background()
	saleID, receiptNo = "sale-return-1", "R-RETURN-1"
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sales(id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at, completed_at)
VALUES(?, ?, 'completed', 'sale', 'GBP', 100, 0, 20, 120, datetime('now'), datetime('now'))`, saleID, receiptNo); err != nil {
		t.Fatal(err)
	}
	lineID = "line-return-1"
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sale_lines(id, sale_id, line_no, item_id, name_snapshot, sku_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
VALUES(?, ?, 1, 'itm1', 'Apple', 'ABC', 1, 100, 2000, 20, 100, 120)`, lineID, saleID); err != nil {
		t.Fatal(err)
	}
	return saleID, receiptNo, lineID
}

func TestCreateReturn_ByOriginalSaleID(t *testing.T) {
	mux, dp := newInventoryAPITestDeps(t)
	saleID, _, lineID := seedCompletedSaleForReturn(t, dp)

	rec := postInvJSON(t, mux, "/api/inventory/return",
		`{"original_sale_id":"`+saleID+`","reason":"faulty","lines":[{"line_id":"`+lineID+`","quantity":1}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"success":true`) {
		t.Fatalf("expected a successful return, got %s", rec.Body.String())
	}
}

func TestCreateReturn_ByReceiptNo(t *testing.T) {
	mux, dp := newInventoryAPITestDeps(t)
	_, receiptNo, lineID := seedCompletedSaleForReturn(t, dp)

	rec := postInvJSON(t, mux, "/api/inventory/return",
		`{"receipt_no":"`+receiptNo+`","reason":"faulty","lines":[{"line_id":"`+lineID+`","quantity":1}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 looking up by receipt_no, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateReturn_ValidationErrors(t *testing.T) {
	mux, dp := newInventoryAPITestDeps(t)
	saleID, _, lineID := seedCompletedSaleForReturn(t, dp)

	if rec := postInvJSON(t, mux, "/api/inventory/return", `{not valid`); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid JSON, got %d", rec.Code)
	}
	if rec := postInvJSON(t, mux, "/api/inventory/return", `{"reason":"x","lines":[{"line_id":"`+lineID+`","quantity":1}]}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing original_sale_id/receipt_no, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec := postInvJSON(t, mux, "/api/inventory/return", `{"original_sale_id":"`+saleID+`","reason":"x","lines":[]}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for zero lines, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec := postInvJSON(t, mux, "/api/inventory/return", `{"receipt_no":"NO-SUCH-RECEIPT","reason":"x","lines":[{"line_id":"x","quantity":1}]}`); rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for an unknown receipt_no, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec := postInvJSON(t, mux, "/api/inventory/return", `{"original_sale_id":"`+saleID+`","reason":"x","lines":[{"line_id":"does-not-exist","quantity":1}]}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unknown line_id, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec := postInvJSON(t, mux, "/api/inventory/return", `{"original_sale_id":"`+saleID+`","reason":"x","lines":[{"line_id":"`+lineID+`","quantity":99}]}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a return quantity exceeding the original, got %d: %s", rec.Code, rec.Body.String())
	}

	// Empty actor id.
	req := httptest.NewRequest(http.MethodPost, "/api/inventory/return", strings.NewReader(`{"original_sale_id":"`+saleID+`","reason":"x","lines":[{"line_id":"`+lineID+`","quantity":1}]}`))
	req.Header.Set("Content-Type", "application/json")
	req = auth.WithUser(req, auth.User{ID: ""})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for an empty actor id, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestGetLowStock_JSONAndHTML(t *testing.T) {
	mux, dp := newInventoryAPITestDeps(t)
	ctx := context.Background()
	// itm1 is seeded at qty 50 with no reorder_level, so it won't be "low" —
	// force a low-stock row directly.
	if _, err := dp.Db.ExecContext(ctx, `UPDATE inventory SET quantity = 1 WHERE item_id = 'itm1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `UPDATE items SET reorder_level = 10 WHERE id = 'itm1'`); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/inventory/low-stock", nil)
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"count"`) {
		t.Fatalf("expected a JSON count field, got %s", rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/inventory/low-stock", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<table") {
		t.Fatalf("expected an HTML table for low-stock items, got %s", rec.Body.String())
	}
}

func TestGetLowStock_EmptyList(t *testing.T) {
	mux, dp := newInventoryAPITestDeps(t)
	ctx := context.Background()
	// Push itm1 comfortably above any reorder threshold.
	if _, err := dp.Db.ExecContext(ctx, `UPDATE items SET reorder_level = 0 WHERE id = 'itm1'`); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/inventory/low-stock", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "No low stock items") {
		t.Fatalf("expected the empty-state message, got %s", rec.Body.String())
	}
}
