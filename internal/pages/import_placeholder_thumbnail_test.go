package pages

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestImport_ImagelessItemsGetPlaceholderThumbnail is the ut-docs#1189
// Phase 1 acceptance test: "import a file with no image column, assert
// every resulting item has a non-empty thumbnail." A generic CSV export
// never carries an image column at all, so every committed row must land
// with SOME thumbnail — the bundled category icon, not a permanently
// blank tile.
func TestImport_ImagelessItemsGetPlaceholderThumbnail(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	csv := "Name,SKU,Price,Category\n" +
		"Cappuccino,C1,2.50,Hot Drinks\n" +
		"Bananas,B1,0.89,Produce\n"
	body, ct := multipartCSV(t, csv, map[string]string{"commit": "1"})
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("commit: code %d body %s", rec.Code, rec.Body.String())
	}

	rows, err := dp.Db.Query(`
SELECT i.sku, img.path FROM items i
LEFT JOIN item_images img ON img.item_id = i.id AND img.role = 'thumbnail'
WHERE i.sku IN ('C1', 'B1')`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	got := map[string]string{}
	for rows.Next() {
		var sku string
		var path *string
		if err := rows.Scan(&sku, &path); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if path == nil {
			t.Fatalf("item %s has NO thumbnail at all — the exact gap this card fixes", sku)
		}
		got[sku] = *path
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 items with thumbnails, got %d: %v", len(got), got)
	}

	// Keyword-matched icons, not just "some non-empty string": Cappuccino
	// (name+category both say coffee) gets the coffee icon; Bananas (no
	// keyword match on either name or category) falls back to generic —
	// both are bundled assets, never a blank tile either way.
	if got["C1"] != "/public/assets/category-icons/coffee.svg" {
		t.Errorf("Cappuccino thumbnail = %q, want the coffee icon", got["C1"])
	}
	if got["B1"] != "/public/assets/category-icons/generic.svg" {
		t.Errorf("Bananas thumbnail = %q, want the generic icon", got["B1"])
	}
}

// TestImport_PlaceholderThumbnailNeverOverwritesReimportedItem: the
// idempotent-reimport path (existing SKU/barcode rows are skipped, per
// TestImport_CommitCreatesCatalog) must never touch an already-set
// thumbnail — including one an operator has since replaced with a real
// photo — since EnsureDefaultThumbnail only runs on rows that actually
// pass through CreateItemTx, which a skipped duplicate never does.
func TestImport_PlaceholderThumbnailNeverOverwritesReimportedItem(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	csv := "Name,SKU,Price,Category\nCappuccino,C1,2.50,Hot Drinks\n"
	body, ct := multipartCSV(t, csv, map[string]string{"commit": "1"})
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("commit: code %d body %s", rec.Code, rec.Body.String())
	}

	var itemID string
	if err := dp.Db.QueryRow(`SELECT id FROM items WHERE sku = 'C1'`).Scan(&itemID); err != nil {
		t.Fatalf("item not created: %v", err)
	}
	if _, err := dp.Db.Exec(
		`UPDATE item_images SET path = ? WHERE item_id = ? AND role = 'thumbnail'`,
		"/public/assets/items/"+itemID+"/thumb.png", itemID,
	); err != nil {
		t.Fatalf("simulate operator photo upload: %v", err)
	}

	body2, ct2 := multipartCSV(t, csv, map[string]string{"commit": "1"})
	req2 := httptest.NewRequest(http.MethodPost, "/api/import", body2)
	req2.Header.Set("Content-Type", ct2)
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("re-commit: code %d body %s", rec2.Code, rec2.Body.String())
	}

	var path string
	if err := dp.Db.QueryRow(
		`SELECT path FROM item_images WHERE item_id = ? AND role = 'thumbnail'`, itemID,
	).Scan(&path); err != nil {
		t.Fatalf("thumbnail row missing after re-import: %v", err)
	}
	if path != "/public/assets/items/"+itemID+"/thumb.png" {
		t.Fatalf("re-import clobbered the operator's real photo: path = %q", path)
	}
}
