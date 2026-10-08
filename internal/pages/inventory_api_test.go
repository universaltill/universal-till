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
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/settings"
)

func newInventoryAPITestDeps(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	// ut-docs#3079: receipt is stock_management-gated now; these
	// tests are about the handlers' own behaviour past that gate (the gate
	// itself: cashier_sale_only_test.go). A test that needs real
	// permissions sets UT_AUTH=on again itself.
	t.Setenv("UT_AUTH", "off")
	chdirRoot(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)

	cfg := &config.Config{
		Theme:   "default",
		Locales: config.Locales{Currency: "GBP", TaxRateBP: 2000},
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

// TestErrorHTML_EscapesMessage is a direct unit test on the shared helper
// respondError's HTML branch goes through (ut-docs#1000).
func TestErrorHTML_EscapesMessage(t *testing.T) {
	got := errorHTML(`<script>alert(1)</script>`)
	want := `<div class='error'>&lt;script&gt;alert(1)&lt;/script&gt;</div>`
	if got != want {
		t.Fatalf("errorHTML did not escape its message:\ngot:  %s\nwant: %s", got, want)
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
	// ut-docs#2011: each row now also carries the item name/SKU/location
	// name a second time, as data-* attributes (so the row can open the
	// receive/adjust dialog prefilled) -- so the payload legitimately
	// appears escaped 6 times now (3 fields × 2 render sites), not 3.
	if got := strings.Count(body, "&lt;script&gt;"); got != 6 {
		t.Fatalf("expected the item name, SKU and location name to be HTML-escaped in both the row's data-* attributes and its visible cells (6 occurrences), got %d in: %s", got, body)
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
	_, queryErr := pos.LowStockItemsFor(context.Background(), dp.Db, "", false)
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

// TestGetLowStock_LocationFilterNotStockedHere covers ut-docs#27: filtered
// to a location, an item stocked only elsewhere is listed at qty 0 and
// marked not stocked here (JSON not_stocked_here, HTML hint) — unless the
// shop sells without tracking stock.
func TestGetLowStock_LocationFilterNotStockedHere(t *testing.T) {
	mux, dp := newInventoryAPITestDeps(t)
	ctx := context.Background()
	// itm1 holds 50 at loc_main; a reorder level of 10 makes it healthy
	// there and not stocked at all at loc_side.
	if _, err := dp.Db.ExecContext(ctx, `UPDATE items SET reorder_level = 10 WHERE id = 'itm1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO stock_locations (id, name) VALUES ('loc_side', 'Side Shop')`); err != nil {
		t.Fatal(err)
	}

	get := func(accept string) string {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/inventory/low-stock?location_id=loc_side", nil)
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}

	var envelope struct {
		Data struct {
			Items []struct {
				ItemID         string  `json:"item_id"`
				VariantID      string  `json:"variant_id"`
				LocationID     string  `json:"location_id"`
				CurrentQty     float64 `json:"current_qty"`
				NotStockedHere bool    `json:"not_stocked_here"`
			} `json:"items"`
		} `json:"data"`
	}
	body := get("application/json")
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	found := false
	for _, it := range envelope.Data.Items {
		if it.ItemID == "itm1" && it.VariantID == "" {
			found = true
			if !it.NotStockedHere || it.CurrentQty != 0 || it.LocationID != "loc_side" {
				t.Fatalf("itm1 at loc_side: %+v, want qty 0, not_stocked_here", it)
			}
		}
	}
	if !found {
		t.Fatalf("itm1 (stocked only at loc_main) must be on loc_side's list, got %s", body)
	}

	if html := get(""); !strings.Contains(html, "Not stocked here") {
		t.Fatalf("HTML list must mark the row not stocked here, got %s", html)
	}

	dp.UpdateState(func(s *common.RuntimeState) { s.AllowNegativeInventory = true })
	if body := get("application/json"); strings.Contains(body, `"not_stocked_here":true`) {
		t.Fatalf("shop sells without tracking stock: no not-stocked-here rows, got %s", body)
	}
}
