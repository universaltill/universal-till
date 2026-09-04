package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
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

// envelopeOf decodes body as the { "data": …, "error": … } shape
// universal-till/CLAUDE.md mandates for every JSON API response, returning
// the raw "data"/"error" values plus whether each key was actually present
// (present-and-null must be distinguishable from absent — a struct-field
// decode alone can't tell the two apart, ut-docs#378).
func envelopeOf(t *testing.T, body []byte) (data json.RawMessage, hasData bool, errVal json.RawMessage, hasError bool) {
	t.Helper()
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("response is not a JSON object: %v (%s)", err, body)
	}
	data, hasData = raw["data"]
	errVal, hasError = raw["error"]
	return
}

func TestCreateStockReceipt_JSONAndHTML(t *testing.T) {
	mux, _ := newInventoryAPITestDeps(t)

	// Positive: JSON accept returns the { "data": …, "error": null } envelope
	// (ut-docs#378), with success=true nested under "data".
	rec := postInvJSON(t, mux, "/api/inventory/receipt", `{"type":"receive","item_id":"itm1","location_id":"loc_main","quantity":5,"reason":"delivery"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	data, hasData, errVal, hasError := envelopeOf(t, rec.Body.Bytes())
	if !hasData || !hasError {
		t.Fatalf("expected a {data,error} envelope, got %s", rec.Body.String())
	}
	if string(errVal) != "null" {
		t.Fatalf("expected error:null on success, got %s", errVal)
	}
	if !strings.Contains(string(data), `"success":true`) {
		t.Fatalf("expected data.success=true, got %s", data)
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
	// Error branch must still use the envelope: an explicit "data":null, not
	// merely an absent data key, plus a non-empty "error" (ut-docs#378).
	data, hasData, errVal, hasError := envelopeOf(t, rec.Body.Bytes())
	if !hasData || string(data) != "null" {
		t.Fatalf(`expected an explicit "data":null, got %s`, rec.Body.String())
	}
	if !hasError || string(errVal) == "null" || string(errVal) == `""` {
		t.Fatalf("expected a non-empty error value, got %s", rec.Body.String())
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

	// ut-docs#780: the audit row must also record the originally-blocked
	// cashier, not just the approving manager — otherwise the row reads
	// as if the manager performed the action directly.
	var dataJSON string
	if err := dp.Db.QueryRowContext(ctx, `SELECT data_json FROM audit_log WHERE id = ?`, overrideID).Scan(&dataJSON); err != nil {
		t.Fatalf("query audit_log data_json for override %q: %v", overrideID, err)
	}
	var payload struct {
		RequestedBy string `json:"requested_by"`
	}
	if err := json.Unmarshal([]byte(dataJSON), &payload); err != nil {
		t.Fatalf("data_json not valid JSON: %v (%s)", err, dataJSON)
	}
	if payload.RequestedBy != "cashier1" {
		t.Fatalf("expected requested_by=%q (the blocked cashier) in the audit payload, got %q (%s)", "cashier1", payload.RequestedBy, dataJSON)
	}
}

// TestCreateNegativeInventoryOverride_AdminSelfApproves_NoRequestedBy covers
// the complementary case (ut-docs#780): when an admin authorizes their own
// override directly (no PIN fallback), requestedBy == actorID and the
// payload must NOT carry a requested_by key at all — the two identities
// coincide, so there is nothing to distinguish.
func TestCreateNegativeInventoryOverride_AdminSelfApproves_NoRequestedBy(t *testing.T) {
	mux, dp := newInventoryAPITestDeps(t)
	ctx := context.Background()

	req := httptest.NewRequest(http.MethodPost, "/api/inventory/override",
		strings.NewReader("reason=stock+count+correction&item_id=itm1&location_id=loc_main&qty_before=50"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = auth.WithUser(req, auth.User{ID: "user1", Role: "admin"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for an admin self-approved override, got %d: %s", rec.Code, rec.Body.String())
	}
	overrideID := strings.TrimSuffix(strings.TrimPrefix(rec.Body.String(), "<div class='success'>Override recorded: "), "</div>")

	var dataJSON string
	if err := dp.Db.QueryRowContext(ctx, `SELECT data_json FROM audit_log WHERE id = ?`, overrideID).Scan(&dataJSON); err != nil {
		t.Fatalf("query audit_log data_json for override %q: %v", overrideID, err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(dataJSON), &payload); err != nil {
		t.Fatalf("data_json not valid JSON: %v (%s)", err, dataJSON)
	}
	if _, present := payload["requested_by"]; present {
		t.Fatalf("expected no requested_by key when actor self-approves, got %s", dataJSON)
	}
}

// TestCreateNegativeInventoryOverride_JSONEnvelope covers the JSON-Accept
// branch specifically -- the other override tests all exercise the
// HTML/HTMX form path, so this is the only coverage of respondOverrideError/
// respondOverrideSuccess's { "data": …, "error": … } envelope (ut-docs#378).
func TestCreateNegativeInventoryOverride_JSONEnvelope(t *testing.T) {
	mux, _ := newInventoryAPITestDeps(t)

	req := httptest.NewRequest(http.MethodPost, "/api/inventory/override",
		strings.NewReader("reason=stock+count+correction&item_id=itm1&location_id=loc_main&qty_before=50"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req = auth.WithUser(req, auth.User{ID: "user1", Role: "admin"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	data, hasData, errVal, hasError := envelopeOf(t, rec.Body.Bytes())
	if !hasData || !hasError {
		t.Fatalf("expected a {data,error} envelope, got %s", rec.Body.String())
	}
	if string(errVal) != "null" {
		t.Fatalf("expected error:null on success, got %s", errVal)
	}
	if !strings.Contains(string(data), `"success":true`) {
		t.Fatalf("expected data.success=true, got %s", data)
	}

	// Error branch: missing reason, same Accept header.
	req = httptest.NewRequest(http.MethodPost, "/api/inventory/override",
		strings.NewReader("item_id=itm1&location_id=loc_main&qty_before=50"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req = auth.WithUser(req, auth.User{ID: "user1", Role: "admin"})
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a missing reason, got %d: %s", rec.Code, rec.Body.String())
	}
	data, hasData, errVal, hasError = envelopeOf(t, rec.Body.Bytes())
	if !hasData || string(data) != "null" {
		t.Fatalf(`expected an explicit "data":null, got %s`, rec.Body.String())
	}
	if !hasError || string(errVal) == "null" || string(errVal) == `""` {
		t.Fatalf("expected a non-empty error value, got %s", rec.Body.String())
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

// TestCreateReturn_LineIDErrorMessageIsHTMLEscaped guards against
// ut-docs#1000: respondReturnError's HTML branch used to interpolate the
// error message (which can embed caller-supplied request fields, e.g. an
// unrecognized line_id) into the response with no escaping. inventory.html's
// return form posts via htmx and app.js force-swaps non-2xx text/html
// responses into the page DOM, so an unescaped line_id containing markup
// would execute in the operator's browser.
func TestCreateReturn_LineIDErrorMessageIsHTMLEscaped(t *testing.T) {
	mux, dp := newInventoryAPITestDeps(t)
	saleID, _, _ := seedCompletedSaleForReturn(t, dp)

	const payload = `<script>alert(1)</script>`
	req := httptest.NewRequest(http.MethodPost, "/api/inventory/return",
		strings.NewReader(`{"original_sale_id":"`+saleID+`","reason":"x","lines":[{"line_id":"`+payload+`","quantity":1}]}`))
	req.Header.Set("Content-Type", "application/json")
	// No "Accept: application/json" — this is the HTML/htmx branch.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for the unrecognized line_id, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, payload) {
		t.Fatalf("line_id was reflected into the HTML response unescaped: %s", body)
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Fatalf("expected the line_id to be HTML-escaped in the response, got %s", body)
	}
}

// TestErrorHTML_EscapesMessage is a direct unit test on the shared helper
// respondError/respondOverrideError/respondReturnError's HTML branches all
// go through (ut-docs#1000) — covers the two call sites
// TestCreateReturn_LineIDErrorMessageIsHTMLEscaped doesn't exercise end-to-end.
func TestErrorHTML_EscapesMessage(t *testing.T) {
	got := errorHTML(`<script>alert(1)</script>`)
	want := `<div class='error'>&lt;script&gt;alert(1)&lt;/script&gt;</div>`
	if got != want {
		t.Fatalf("errorHTML did not escape its message:\ngot:  %s\nwant: %s", got, want)
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
	// { "data": …, "error": null } envelope (ut-docs#378), success nested
	// under "data" rather than the old bare top-level shape.
	data, hasData, errVal, hasError := envelopeOf(t, rec.Body.Bytes())
	if !hasData || !hasError {
		t.Fatalf("expected a {data,error} envelope, got %s", rec.Body.String())
	}
	if string(errVal) != "null" {
		t.Fatalf("expected error:null on success, got %s", errVal)
	}
	if !strings.Contains(string(data), `"success":true`) {
		t.Fatalf("expected a successful return, got %s", data)
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

// seedInclusiveEURSaleForReturn seeds a German-shop-shaped original sale —
// EUR currency, VAT-inclusive pricing (unit_price is the gross, tax-included
// price, and the sale header's own arithmetic makes
// pos.InferTaxInclusive/saleIsTaxInclusive read it as inclusive) — for
// ut-docs#1494's regression coverage: CreateReturn must derive the return's
// Currency/TaxInclusive from THIS sale, not the English-market default.
func seedInclusiveEURSaleForReturn(t *testing.T, dp *common.Deps) (saleID, lineID string) {
	t.Helper()
	ctx := context.Background()
	saleID = "sale-return-eur-inclusive"
	// subtotal(120) == subtotal - discount(0) + serviceCharge(0) +
	// voucherIssueTotal(0) with taxTotal(20) != 0 -> InferTaxInclusive true.
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sales(id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at, completed_at)
VALUES(?, 'R-RETURN-EUR-1', 'completed', 'sale', 'EUR', 120, 0, 20, 120, datetime('now'), datetime('now'))`, saleID); err != nil {
		t.Fatal(err)
	}
	lineID = "line-return-eur-1"
	// unit_price is the gross (tax-inclusive) selling price, as an
	// inclusive-priced shop's catalog prices are.
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sale_lines(id, sale_id, line_no, item_id, name_snapshot, sku_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
VALUES(?, ?, 1, 'itm1', 'Apple', 'ABC', 1, 120, 2000, 20, 100, 120)`, lineID, saleID); err != nil {
		t.Fatal(err)
	}
	return saleID, lineID
}

// TestCreateReturn_UsesOriginalSaleCurrencyAndTaxMode is the regression test
// for ut-docs#1494: CreateReturn used to hardcode Currency:"GBP" and leave
// TaxInclusive at its false zero value regardless of the original sale, so a
// German (EUR, inclusive-priced) shop's return was signed and persisted as
// "currency":"GBP","tax_inclusive":false. It must instead derive both from
// the original sale, same source refund_page.go's sibling flow reads.
func TestCreateReturn_UsesOriginalSaleCurrencyAndTaxMode(t *testing.T) {
	mux, dp := newInventoryAPITestDeps(t)
	saleID, lineID := seedInclusiveEURSaleForReturn(t, dp)

	rec := postInvJSON(t, mux, "/api/inventory/return",
		`{"original_sale_id":"`+saleID+`","reason":"faulty","lines":[{"line_id":"`+lineID+`","quantity":1}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	respData, hasData, errVal, hasError := envelopeOf(t, rec.Body.Bytes())
	if !hasData || !hasError {
		t.Fatalf("expected a {data,error} envelope, got %s", rec.Body.String())
	}
	if string(errVal) != "null" {
		t.Fatalf("expected error:null on success, got %s: full body %s", errVal, rec.Body.String())
	}
	var parsed struct {
		ReturnSaleID string `json:"return_sale_id"`
	}
	if err := json.Unmarshal(respData, &parsed); err != nil || parsed.ReturnSaleID == "" {
		t.Fatalf("expected a return_sale_id in the response data, got %s (err: %v)", respData, err)
	}

	repo := data.NewPOSRepo(dp.Db)
	returnDetail, found, err := repo.GetSaleDetailByID(context.Background(), parsed.ReturnSaleID)
	if err != nil || !found {
		t.Fatalf("expected to find the persisted return sale %q: found=%v err=%v", parsed.ReturnSaleID, found, err)
	}
	if returnDetail.Currency != "EUR" {
		t.Fatalf("return was persisted as currency %q, want EUR (from the original sale) — the ut-docs#1494 hardcoded-GBP bug", returnDetail.Currency)
	}
	if !saleIsTaxInclusive(returnDetail) {
		t.Fatalf("return sale %+v does not read back as tax-inclusive, want it to match the original EUR-inclusive sale's pricing mode", returnDetail)
	}
}

func TestCreateReturn_ValidationErrors(t *testing.T) {
	mux, dp := newInventoryAPITestDeps(t)
	saleID, _, lineID := seedCompletedSaleForReturn(t, dp)

	if rec := postInvJSON(t, mux, "/api/inventory/return", `{not valid`); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid JSON, got %d", rec.Code)
	} else if data, hasData, errVal, hasError := envelopeOf(t, rec.Body.Bytes()); !hasData || string(data) != "null" || !hasError || string(errVal) == "null" {
		t.Fatalf(`expected { "data": null, "error": "…" } for invalid JSON, got %s`, rec.Body.String())
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
	// original_sale_id that parses but matches no row (ut-docs#1494's new
	// GetSaleDetailByID lookup, ahead of ListSaleLineSnapshots).
	if rec := postInvJSON(t, mux, "/api/inventory/return", `{"original_sale_id":"no-such-sale-id","reason":"x","lines":[{"line_id":"x","quantity":1}]}`); rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for an unknown original_sale_id, got %d: %s", rec.Code, rec.Body.String())
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
	// The whole product wraps JSON API responses as { "data": …, "error": null }
	// (universal-till/CLAUDE.md, "API, formats, i18n") -- this endpoint must
	// follow the same envelope every other JSON handler in this package uses,
	// not return its payload bare at the top level.
	var envelope struct {
		Data *struct {
			Items json.RawMessage `json:"items"`
			Count int             `json:"count"`
		} `json:"data"`
		Error *string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("response is not the {data,error} JSON envelope: %v (%s)", err, rec.Body.String())
	}
	if envelope.Error != nil {
		t.Fatalf("expected error:null on success, got %q", *envelope.Error)
	}
	if envelope.Data == nil {
		t.Fatalf("expected a data field wrapping items/count, got %s", rec.Body.String())
	}
	if envelope.Data.Count != 1 {
		t.Fatalf("expected data.count == 1, got %d (%s)", envelope.Data.Count, rec.Body.String())
	}
	if !strings.Contains(string(envelope.Data.Items), "itm1") {
		t.Fatalf("expected data.items to contain the low-stock item, got %s", envelope.Data.Items)
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

// TestGetLowStock_HTMLTableEscapesItemFields guards against ut-docs#1019:
// GetLowStock's success-path HTML table interpolated item.Name/SKU/
// LocationName with no escaping, unlike its error branches (ut-docs#1000,
// TestGetLowStock_HTMLError_UsesErrorHTMLHelper). Unlike the error
// branches, these values come from persisted catalog/location data — set
// via catalog admin or an import — rather than an immediate request-echo,
// making this stored-XSS-shaped: a markup-bearing item/location name would
// render unescaped into an authenticated operator's low-stock table.
func TestGetLowStock_HTMLTableEscapesItemFields(t *testing.T) {
	mux, dp := newInventoryAPITestDeps(t)
	ctx := context.Background()

	const payload = `<script>alert(1)</script>`
	if _, err := dp.Db.ExecContext(ctx, `UPDATE items SET name = ?, sku = ?, reorder_level = 10 WHERE id = 'itm1'`, payload, payload); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `UPDATE stock_locations SET name = ? WHERE id = 'loc_main'`, payload); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `UPDATE inventory SET quantity = 1 WHERE item_id = 'itm1'`); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/inventory/low-stock", nil)
	// No "Accept: application/json" — this is the HTML/htmx branch.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, payload) {
		t.Fatalf("item/SKU/location name was interpolated into the HTML table unescaped: %s", body)
	}
	if got := strings.Count(body, "&lt;script&gt;"); got != 3 {
		t.Fatalf("expected the item name, SKU and location name (3 occurrences) to be HTML-escaped, got %d in: %s", got, body)
	}
}

// TestGetLowStock_JSONError_UsesDataErrorEnvelope covers the error branch of
// the same handler: a query failure must still respond as { "data": null,
// "error": "…" }, not a bare { "error": "…" } object.
func TestGetLowStock_JSONError_UsesDataErrorEnvelope(t *testing.T) {
	mux, dp := newInventoryAPITestDeps(t)
	dp.Db.Close() // closed *sql.DB forces GetLowStockItems's query to fail deterministically

	req := httptest.NewRequest(http.MethodGet, "/api/inventory/low-stock", nil)
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("response is not a JSON object: %v (%s)", err, rec.Body.String())
	}
	// Assert the "data" key is actually present (and null), not merely
	// absent -- a response with no "data" key at all would also decode as
	// a nil field into a Go struct, silently passing a weaker check.
	dataVal, hasData := raw["data"]
	if !hasData {
		t.Fatalf("expected an explicit \"data\" key (null) in the error envelope, got %s", rec.Body.String())
	}
	if string(dataVal) != "null" {
		t.Fatalf("expected data:null on error, got %s", dataVal)
	}
	errVal, hasError := raw["error"]
	if !hasError || string(errVal) == "null" || string(errVal) == `""` {
		t.Fatalf("expected a non-empty \"error\" value, got %s", rec.Body.String())
	}
}

// TestGetLowStock_HTMLError_UsesErrorHTMLHelper covers the HTML branch of
// the same error path TestGetLowStock_JSONError_UsesDataErrorEnvelope covers
// for JSON: found during independent review of ut-docs#1000's fix as the
// same unescaped-interpolation shape left behind at this call site (query
// errors here aren't currently attacker-echoed text, but the response must
// go through the same escaping helper as every other respond*Error branch
// in this file, not a bespoke fmt.Sprintf, so a future caller-influenced
// error message here doesn't reopen the gap).
func TestGetLowStock_HTMLError_UsesErrorHTMLHelper(t *testing.T) {
	mux, dp := newInventoryAPITestDeps(t)
	dp.Db.Close() // closed *sql.DB forces GetLowStockItems's query to fail deterministically

	req := httptest.NewRequest(http.MethodGet, "/api/inventory/low-stock", nil)
	// No "Accept: application/json" — this is the HTML/htmx branch.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
	_, queryErr := pos.GetLowStockItems(context.Background(), dp.Db, "")
	if queryErr == nil {
		t.Fatal("expected the closed *sql.DB to still fail the same query")
	}
	if want := errorHTML(queryErr.Error()); rec.Body.String() != want {
		t.Fatalf("expected the HTML error body to be built by errorHTML(), got %s, want %s", rec.Body.String(), want)
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
