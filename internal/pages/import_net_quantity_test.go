package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/catimport"
	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#3403: writeCatalogCSV's "Net quantity"/"Net quantity unit"
// columns must come back through our own importer exactly — value and
// unit — and an item with no net quantity must come back with none.
func TestExportCSVRoundTripsNetQuantity(t *testing.T) {
	grams, millilitres, each := int64(250), int64(1500), int64(6)
	g, ml, ea := catalogtypes.NetQuantityGrams, catalogtypes.NetQuantityMillilitres, catalogtypes.NetQuantityEach
	rows := []data.ExportRow{
		{Name: "Coffee Beans", SKU: "NQ-1", PriceMinor: 650, NetQuantityValue: &grams, NetQuantityUnit: &g},
		{Name: "Olive Oil", SKU: "NQ-2", PriceMinor: 899, NetQuantityValue: &millilitres, NetQuantityUnit: &ml},
		{Name: "Eggs", SKU: "NQ-3", PriceMinor: 210, NetQuantityValue: &each, NetQuantityUnit: &ea},
		{Name: "Loose Item", SKU: "NQ-4", PriceMinor: 100},
	}

	var b strings.Builder
	writeCatalogCSV(&b, rows, 2)

	res, err := catimport.Parse(strings.NewReader(b.String()), 2, data.DefaultEnabledBarcodeSymbologyIDs(), false)
	if err != nil {
		t.Fatalf("parse exported CSV: %v", err)
	}
	if len(res.Items) != len(rows) {
		t.Fatalf("expected %d items back, got %d", len(rows), len(res.Items))
	}
	for i, want := range rows[:3] {
		got := res.Items[i]
		if got.NetQuantityValue == nil || got.NetQuantityUnit == nil ||
			*got.NetQuantityValue != *want.NetQuantityValue || *got.NetQuantityUnit != *want.NetQuantityUnit {
			t.Errorf("%s: net quantity did not round-trip: %+v", want.Name, got)
		}
		if got.NetQuantityIssue != "" || got.Issue != "" {
			t.Errorf("%s: round-trip row carries issues: %q %q", want.Name, got.NetQuantityIssue, got.Issue)
		}
	}
	loose := res.Items[3]
	if loose.NetQuantityValue != nil || loose.NetQuantityUnit != nil || loose.NetQuantityIssue != "" {
		t.Errorf("item without a net quantity must come back without one: %+v", loose)
	}
}

// ut-docs#3403's acceptance criterion, end to end through the real HTTP
// handlers and a real migrated database: an item with a configured net
// quantity is exported, the catalog is wiped, the CSV is imported back,
// and the re-imported item carries the identical value and unit.
func TestCatalogExport_NetQuantitySurvivesExportWipeReimport(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	if _, err := dp.Db.Exec(
		`INSERT INTO items(id,sku,name,base_price,is_active,net_quantity_value,net_quantity_unit) VALUES('itmNQ','NQ1','Ground Coffee',650,1,227,'g')`); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	if _, err := dp.Db.Exec(
		`INSERT INTO items(id,sku,name,base_price,is_active) VALUES('itmNone','NQ2','Plain Item',100,1)`); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	mux := http.NewServeMux()
	registerImport(mux, dp)
	req := httptest.NewRequest(http.MethodGet, "/api/catalog/export", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("export: code %d body %s", rec.Code, rec.Body.String())
	}
	exported := rec.Body.String()
	if !strings.HasPrefix(exported, "Name,SKU,Barcode,Price,Category,Description,Sold by weight,In stock,Active,Tax rate,Takeaway tax,Net quantity,Net quantity unit") {
		t.Fatalf("export header missing the net quantity columns: %q", exported)
	}

	// Wipe: the seeded items have no barcodes, stock or price history, so
	// removing the two rows leaves the catalog genuinely empty.
	if _, err := dp.Db.Exec(`DELETE FROM items WHERE id IN ('itmNQ','itmNone')`); err != nil {
		t.Fatalf("wipe catalog: %v", err)
	}

	body, ct := multipartCSV(t, exported, map[string]string{"commit": "1"})
	req = httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("import: code %d body %s", rec.Code, rec.Body.String())
	}

	var value int64
	var unit string
	if err := dp.Db.QueryRow(`SELECT net_quantity_value, net_quantity_unit FROM items WHERE sku = 'NQ1'`).Scan(&value, &unit); err != nil {
		t.Fatalf("imported item should carry a net quantity: %v", err)
	}
	if value != 227 || unit != "g" {
		t.Errorf("net quantity after export → import = %d %q, want 227 \"g\"", value, unit)
	}
	var noneValue, noneUnit any
	if err := dp.Db.QueryRow(`SELECT net_quantity_value, net_quantity_unit FROM items WHERE sku = 'NQ2'`).Scan(&noneValue, &noneUnit); err != nil {
		t.Fatalf("plain item should be imported: %v", err)
	}
	if noneValue != nil || noneUnit != nil {
		t.Errorf("item without a net quantity must import without one, got %v %v", noneValue, noneUnit)
	}
}

// An invalid net quantity (a unit outside g/ml/ea) must not block the row
// and must not be stored — the item imports with none — but the drop is
// warned about on the row, never silent.
func TestImport_InvalidNetQuantityWarnsButStillImports(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	initAuthTestI18n(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	csv := "Name,SKU,Price,Net quantity,Net quantity unit\n" +
		"Flour,NQ9,1.20,1500,kg\n"
	body, ct := multipartCSV(t, csv, map[string]string{"commit": "1"})
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("commit: code %d body %s", rec.Code, rec.Body.String())
	}
	resp := rec.Body.String()

	var value, unit any
	if err := dp.Db.QueryRow(`SELECT net_quantity_value, net_quantity_unit FROM items WHERE sku = 'NQ9'`).Scan(&value, &unit); err != nil {
		t.Fatalf("item should still be created despite the bad net quantity: %v", err)
	}
	if value != nil || unit != nil {
		t.Fatalf("invalid net quantity must not be stored, got %v %v", value, unit)
	}
	if !strings.Contains(resp, "1500 kg") {
		t.Fatalf("row should warn with the refused net quantity, got: %s", resp)
	}
	if !strings.Contains(resp, `class="row-warn"`) {
		t.Fatalf("bad net-quantity row must carry the warned visual treatment, got: %s", resp)
	}
}

// ut-docs#3473: the exact scenario the card describes end to end through
// the real HTTP handler — a merchant opens this till's own CSV export in a
// spreadsheet app, saves it as .xlsx, and re-uploads it. Net quantity must
// survive that round trip exactly like it already does for a re-uploaded
// CSV (TestExportCSVRoundTripsNetQuantity above).
func TestImport_XLSXUploadImportsNetQuantity(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	xlsxBytes := buildXLSXForPagesTest(t, [][]string{
		{"Name", "SKU", "Price", "Net quantity", "Net quantity unit"},
		{"Ground Coffee", "NQX-1", "6.50", "227", "g"},
		{"Plain Item", "NQX-2", "1.00", "", ""},
	})
	body, ct := multipartFile(t, "catalog.xlsx", xlsxBytes, map[string]string{"commit": "1"})
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("commit: code %d body %s", rec.Code, rec.Body.String())
	}

	var value int64
	var unit string
	if err := dp.Db.QueryRow(`SELECT net_quantity_value, net_quantity_unit FROM items WHERE sku = 'NQX-1'`).Scan(&value, &unit); err != nil {
		t.Fatalf("imported item should carry a net quantity: %v", err)
	}
	if value != 227 || unit != "g" {
		t.Errorf("net quantity after xlsx import = %d %q, want 227 \"g\"", value, unit)
	}
	var noneValue, noneUnit any
	if err := dp.Db.QueryRow(`SELECT net_quantity_value, net_quantity_unit FROM items WHERE sku = 'NQX-2'`).Scan(&noneValue, &noneUnit); err != nil {
		t.Fatalf("plain item should be imported: %v", err)
	}
	if noneValue != nil || noneUnit != nil {
		t.Errorf("item without a net quantity must import without one, got %v %v", noneValue, noneUnit)
	}
}
