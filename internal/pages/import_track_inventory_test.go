package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ut-docs#1843: a source system that says "Track inventory? No" for an item
// is stating that its Quantity column is not a stock level. Importing that
// number would invent an on-hand figure the source itself disclaims, and the
// German pilot merchant's real SumUp export carries exactly this shape (No +
// a quantity, for all 116 items).
//
// Mirrors TestImport_NegativeStockQuantityWarns: the item must still import,
// no stock may be carried, and the operator must be TOLD, not silently
// deprived of a number that was in their file.
func TestImport_TrackInventoryNoDoesNotCarryStock(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	csv := "Name,SKU,Barcode,Price,Category,In stock,Track inventory? (Yes/No)\n" +
		"Widget,W1,5012345678900,1.50,Snacks,4,No\n"
	body, ct := multipartCSV(t, csv, map[string]string{"commit": "1"})
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("commit: code %d body %s", rec.Code, rec.Body.String())
	}
	resp := rec.Body.String()

	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM items WHERE sku = 'W1'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("item should still be created (n=%d err=%v)", n, err)
	}
	if !strings.Contains(resp, "not stock-tracked") {
		t.Fatalf("row should say the stock was not carried and why, got: %s", resp)
	}
	// The point of the whole change: the source's quantity is not recorded
	// as an on-hand level.
	//
	// Note the row itself still EXISTS at zero: CatalogRepo.CreateItemTx
	// calls ensureInventoryRowExec for every item it creates, independently
	// of any stock movement, so "no inventory row at all" is not reachable
	// from the import path (ut-docs#1843's card body assumed it was — the
	// merchant's symptom is identical either way, because a row at 0 fails
	// the same `cur + qtyDelta < 0` guard a missing row does). Asserting the
	// quantity, not the row count, is what actually encodes the promise.
	var qty float64
	if err := dp.Db.QueryRow(`SELECT COALESCE(SUM(quantity),0) FROM inventory WHERE item_id = (SELECT id FROM items WHERE sku='W1')`).Scan(&qty); err != nil {
		t.Fatalf("read inventory: %v", err)
	}
	if qty != 0 {
		t.Fatalf("a quantity the source disclaims must not become stock, got qty=%v", qty)
	}
	var moves int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM stock_movements WHERE item_id = (SELECT id FROM items WHERE sku='W1')`).Scan(&moves); err != nil {
		t.Fatalf("read stock_movements: %v", err)
	}
	if moves != 0 {
		t.Fatalf("no opening-stock movement may be recorded for an untracked item, got %d", moves)
	}
}

// The same file WITHOUT the column must be completely unaffected — the
// regression guard for every existing Loyverse/Square/SumUp export.
func TestImport_NoTrackInventoryColumnStillCarriesStock(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	csv := "Name,SKU,Barcode,Price,Category,In stock\n" +
		"Widget,W1,5012345678900,1.50,Snacks,4\n"
	body, ct := multipartCSV(t, csv, map[string]string{"commit": "1"})
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("commit: code %d body %s", rec.Code, rec.Body.String())
	}

	var qty float64
	if err := dp.Db.QueryRow(`SELECT COALESCE(SUM(quantity),0) FROM inventory WHERE item_id = (SELECT id FROM items WHERE sku='W1')`).Scan(&qty); err != nil {
		t.Fatalf("read inventory: %v", err)
	}
	if qty != 4 {
		t.Fatalf("opening stock must still be carried when the file says nothing about tracking, got %v", qty)
	}
}

// And "Yes" must carry the stock, so the column is honoured in both
// directions rather than being a one-way off switch.
func TestImport_TrackInventoryYesCarriesStock(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	csv := "Name,SKU,Barcode,Price,Category,In stock,Track inventory? (Yes/No)\n" +
		"Widget,W1,5012345678900,1.50,Snacks,4,Yes\n"
	body, ct := multipartCSV(t, csv, map[string]string{"commit": "1"})
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("commit: code %d body %s", rec.Code, rec.Body.String())
	}

	var qty float64
	if err := dp.Db.QueryRow(`SELECT COALESCE(SUM(quantity),0) FROM inventory WHERE item_id = (SELECT id FROM items WHERE sku='W1')`).Scan(&qty); err != nil {
		t.Fatalf("read inventory: %v", err)
	}
	if qty != 4 {
		t.Fatalf("a tracked item must carry its opening stock, got %v", qty)
	}
}
