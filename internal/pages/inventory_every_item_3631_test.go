package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ut-docs#3631: the /inventory page lists every active, stock-tracked item —
// one never received (no inventory row at all, e.g. imported or synced in)
// shows qty 0 at the Main location — because tapping a row is now the only
// way into the receive/adjust dialog. The page-head "+" button, the manager
// negative-stock override panel and the "Process a return" panel are gone,
// with their endpoints.

func TestInventoryPage_ListsNeverStockedItem(t *testing.T) {
	mux, dp := newInventoryAPITestDeps(t)
	registerInventoryPage(mux, dp)
	if _, err := dp.Db.ExecContext(t.Context(),
		`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm-never', 'NEV-1', 'Never Received Widget', 100, 1)`); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"/inventory", "/ui/inventory/stock-table"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: code %d body %s", path, rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		if !strings.Contains(body, `data-item="itm-never"`) {
			t.Fatalf("GET %s: a never-stocked item must get a stock row, got: %s", path, body)
		}
		if !strings.Contains(body, `data-item="itm-never" data-name="Never Received Widget" data-sku="NEV-1" data-location="loc_main"`) {
			t.Fatalf("GET %s: the never-stocked item's row must carry the Main location for the dialog prefill, got: %s", path, body)
		}
	}
}

func TestInventoryPage_RemovedControlsAreGone(t *testing.T) {
	mux, dp := newInventoryAPITestDeps(t)
	registerInventoryPage(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/inventory", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /inventory: code %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, gone := range []string{
		`stock-dialog-open`, `override-form`, `return-form`,
		`/api/inventory/override`, `/api/inventory/return`,
	} {
		if strings.Contains(body, gone) {
			t.Fatalf("/inventory must no longer contain %q (ut-docs#3631)", gone)
		}
	}
	// The dialog itself stays, reached by tapping a row.
	if !strings.Contains(body, `id="stock-dialog"`) || !strings.Contains(body, `id="stock-form"`) {
		t.Fatalf("the receive/adjust dialog must stay, got: %s", body)
	}
}

func TestInventoryAPI_RemovedEndpointsAreGone(t *testing.T) {
	mux, _ := newInventoryAPITestDeps(t)
	for _, rt := range []struct{ method, path string }{
		{http.MethodPost, "/api/inventory/override"},
		{http.MethodPost, "/api/inventory/return"},
		{http.MethodGet, "/api/inventory/return/lines?receipt_no=1"},
	} {
		req := httptest.NewRequest(rt.method, rt.path, strings.NewReader("reason=x"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s %s must be gone (404/405), got %d: %s", rt.method, rt.path, rec.Code, rec.Body.String())
		}
	}
}
