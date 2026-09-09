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
	// As of ut-docs#1850, this specific row no longer exists at all:
	// CreateItemTx now skips ensureInventoryRowExec for a StockUntracked
	// item (this test's "No" answer persists onto the item, see
	// TestImport_TrackInventoryNoPersistsItemAsStockUntracked below), so
	// there is genuinely no inventory row, not a row pinned at zero.
	// COALESCE(SUM(...),0) reads 0 either way, so this assertion alone
	// can't tell the two apart — that's exactly why the dedicated
	// stock_untracked assertion exists as its own test.
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

// TestImport_TrackInventoryNoPersistsItemAsStockUntracked covers ut-docs#1850
// step 2: the "Track inventory? No" answer must persist onto the created
// item's own stock_untracked flag, not just skip this one opening-stock
// movement — otherwise a later sale of the same item would still drive it
// negative and it would still show up in low-stock lists.
func TestImport_TrackInventoryNoPersistsItemAsStockUntracked(t *testing.T) {
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

	var untracked bool
	if err := dp.Db.QueryRow(`SELECT stock_untracked FROM items WHERE sku='W1'`).Scan(&untracked); err != nil {
		t.Fatalf("read stock_untracked: %v", err)
	}
	if !untracked {
		t.Fatalf("expected the imported item to persist as stock_untracked=true, got false")
	}
}

// The "Yes" case is the regression guard: an explicitly tracked import must
// NOT be marked untracked.
func TestImport_TrackInventoryYesPersistsItemAsTracked(t *testing.T) {
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

	var untracked bool
	if err := dp.Db.QueryRow(`SELECT stock_untracked FROM items WHERE sku='W1'`).Scan(&untracked); err != nil {
		t.Fatalf("read stock_untracked: %v", err)
	}
	if untracked {
		t.Fatalf("expected the imported item to persist as stock_untracked=false, got true")
	}
}
