package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ut-docs#3901: since #3631 every active, stock-tracked item gets a stock
// row (at 0 if never received), so the table's empty row only shows when the
// shop has no stock-tracked items at all. Its text must say that and what to
// do next — not "No stock recorded yet", which a never-received item now
// contradicts.

const inventoryEmptyText = "No stock-tracked items yet."

func inventoryBodies(t *testing.T, mux http.Handler) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, path := range []string{"/inventory", "/ui/inventory/stock-table"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: code %d body %s", path, rec.Code, rec.Body.String())
		}
		out[path] = rec.Body.String()
	}
	return out
}

func TestInventoryEmptyState_NoTrackedItems(t *testing.T) {
	mux, dp := newInventoryAPITestDeps(t)
	registerInventoryPage(mux, dp)
	// Retire the seeded catalogue, then add only an untracked item: it never
	// shows in Inventory, so the table is empty.
	if _, err := dp.Db.ExecContext(t.Context(), `UPDATE items SET is_active = 0`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(t.Context(),
		`INSERT INTO items (id, sku, name, base_price, is_active, stock_untracked) VALUES ('itm-keg', 'KEG-1', 'Keg', 100, 1, 1)`); err != nil {
		t.Fatal(err)
	}
	for path, body := range inventoryBodies(t, mux) {
		if !strings.Contains(body, inventoryEmptyText) {
			t.Fatalf("GET %s: empty table must say %q, got: %s", path, inventoryEmptyText, body)
		}
		if strings.Contains(body, "No stock recorded yet") {
			t.Fatalf("GET %s: the old empty-state wording must be gone (ut-docs#3901)", path)
		}
	}
}

func TestInventoryEmptyState_HiddenWhenATrackedItemExists(t *testing.T) {
	mux, dp := newInventoryAPITestDeps(t)
	registerInventoryPage(mux, dp)
	if _, err := dp.Db.ExecContext(t.Context(), `UPDATE items SET is_active = 0`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(t.Context(),
		`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm-zero', 'ZER-1', 'Zero Widget', 100, 1)`); err != nil {
		t.Fatal(err)
	}
	for path, body := range inventoryBodies(t, mux) {
		if strings.Contains(body, inventoryEmptyText) {
			t.Fatalf("GET %s: a tracked item at 0 is a row, so the empty row must not show", path)
		}
	}
}
