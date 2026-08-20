package pages

import (
	"archive/zip"
	"bytes"
	"database/sql"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// ut-docs#511: /api/import must auto-detect a speedy kasse / pepperm
// cashbox .bkp upload (a plain ZIP) alongside the existing CSV path, with
// no separate route. These tests build a synthetic .bkp — never real
// customer data — the same way internal/catimport/bkp_test.go does.

// buildBkpDBBytesForPagesTest creates a temp SQLite file with a Products
// table matching the documented speedy kasse schema and returns its bytes.
func buildBkpDBBytesForPagesTest(t *testing.T) []byte {
	t.Helper()
	return buildBkpDBBytesForPagesTestWithTaxRows(t, nil)
}

// bkpTaxRow is one Products row carrying the ut-docs#512 tax columns, for
// the .bkp tax-carry-through end-to-end test below.
type bkpTaxRow struct {
	ProductNumber, Name, Category string
	Price                         float64
	TaxPct, TaxPct2               any
}

// buildBkpDBBytesForPagesTestWithTaxRows is buildBkpDBBytesForPagesTest's
// superset: same base fixture (Latte + a deleted row), plus any extra rows
// the caller supplies, on a Products schema that also carries
// TaxPercentage/TaxPercentage2 (ut-docs#512).
func buildBkpDBBytesForPagesTestWithTaxRows(t *testing.T, extra []bkpTaxRow) []byte {
	t.Helper()
	tmp, err := os.CreateTemp("", "bkp-pages-fixture-*.db")
	if err != nil {
		t.Fatalf("create temp db: %v", err)
	}
	path := tmp.Name()
	_ = tmp.Close()
	t.Cleanup(func() { _ = os.Remove(path) })

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open temp db: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE Products (
		ProductNumber TEXT,
		ProductTextShort TEXT,
		SalesPrice REAL,
		ProductGroupText TEXT,
		Status INTEGER,
		ProductType INTEGER,
		TaxPercentage REAL,
		TaxPercentage2 REAL
	)`); err != nil {
		t.Fatalf("create Products table: %v", err)
	}
	rows := [][8]any{
		{"20001", "Latte", 3.20, "Coffee", 1, 1, nil, nil},
		{"20002", "Old Deleted Item", 1.00, "Misc", 3, 1, nil, nil},
	}
	for _, r := range rows {
		if _, err := db.Exec(`INSERT INTO Products
			(ProductNumber, ProductTextShort, SalesPrice, ProductGroupText, Status, ProductType, TaxPercentage, TaxPercentage2)
			VALUES (?,?,?,?,?,?,?,?)`, r[0], r[1], r[2], r[3], r[4], r[5], r[6], r[7]); err != nil {
			t.Fatalf("insert product: %v", err)
		}
	}
	for _, r := range extra {
		if _, err := db.Exec(`INSERT INTO Products
			(ProductNumber, ProductTextShort, SalesPrice, ProductGroupText, Status, ProductType, TaxPercentage, TaxPercentage2)
			VALUES (?,?,?,?,1,1,?,?)`, r.ProductNumber, r.Name, r.Price, r.Category, r.TaxPct, r.TaxPct2); err != nil {
			t.Fatalf("insert tax product: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close temp db: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read temp db bytes: %v", err)
	}
	return b
}

// buildBkpZipForPagesTest assembles an in-memory .bkp-shaped ZIP (backup.db
// + meta.inf, no recognisable checksum structure — the meta.inf validator's
// best-effort path, exercised separately by internal/catimport/bkp_test.go).
func buildBkpZipForPagesTest(t *testing.T) []byte {
	t.Helper()
	dbBytes := buildBkpDBBytesForPagesTest(t)
	return zipBkpEntries(t, dbBytes)
}

// buildBkpZipForPagesTestWithTaxRows is buildBkpZipForPagesTest's
// tax-carrying counterpart.
func buildBkpZipForPagesTestWithTaxRows(t *testing.T, extra []bkpTaxRow) []byte {
	t.Helper()
	dbBytes := buildBkpDBBytesForPagesTestWithTaxRows(t, extra)
	return zipBkpEntries(t, dbBytes)
}

func zipBkpEntries(t *testing.T, dbBytes []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	dbW, err := zw.Create("backup.db")
	if err != nil {
		t.Fatalf("create backup.db entry: %v", err)
	}
	if _, err := dbW.Write(dbBytes); err != nil {
		t.Fatalf("write backup.db entry: %v", err)
	}
	metaW, err := zw.Create("meta.inf")
	if err != nil {
		t.Fatalf("create meta.inf entry: %v", err)
	}
	if _, err := metaW.Write([]byte(`{"app_version":"4.4.08","device":"iMin I22T01","licence_key":"TEST-0000"}`)); err != nil {
		t.Fatalf("write meta.inf entry: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
	return buf.Bytes()
}

// multipartFile builds a multipart body with raw bytes in "file" under the
// given filename — multipartCSV's counterpart for non-CSV payloads.
func multipartFile(t *testing.T, filename string, content []byte, fields map[string]string) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatalf("write file: %v", err)
	}
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			t.Fatalf("write field: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return &buf, w.FormDataContentType()
}

// TestImport_AutoDetectsBkpUpload_Preview covers the auto-detect wiring
// itself: a .bkp-shaped upload must be routed through catimport.ParseBkp,
// not catimport.Parse. Parse would either error out on binary zip bytes (no
// name column / a csv read failure) or, if it somehow didn't, would never
// produce "speedy-kasse" as the detected format — so asserting that string
// in the preview response is a real discriminator between the two code
// paths, not just a happy-path smoke test.
func TestImport_AutoDetectsBkpUpload_Preview(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	zipBytes := buildBkpZipForPagesTest(t)
	body, ct := multipartFile(t, "Backup 2026-08-09.bkp", zipBytes, nil) // no commit
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("preview: code %d body %s", rec.Code, rec.Body.String())
	}
	resp := rec.Body.String()
	if !strings.Contains(resp, "speedy-kasse") {
		t.Fatalf("response should show the speedy-kasse format (proves ParseBkp ran, not Parse), got: %s", resp)
	}
	if !strings.Contains(resp, "Latte") {
		t.Fatalf("preview should list the parsed bkp row, got: %s", resp)
	}
}

// TestImport_AutoDetectsBkpUpload_Commit covers the full commit path for a
// .bkp upload: the sellable row is created (name/price/category mapped, no
// barcode), and the Status=3 row is excluded.
func TestImport_AutoDetectsBkpUpload_Commit(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	zipBytes := buildBkpZipForPagesTest(t)
	body, ct := multipartFile(t, "Backup 2026-08-09.bkp", zipBytes, map[string]string{"commit": "1"})
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("commit: code %d body %s", rec.Code, rec.Body.String())
	}

	var name, catName string
	var price int64
	if err := dp.Db.QueryRow(`SELECT i.name, i.base_price, c.name FROM items i JOIN categories c ON c.id = i.category_id WHERE i.sku = '20001'`).Scan(&name, &price, &catName); err != nil {
		t.Fatalf("Latte not created: %v", err)
	}
	if name != "Latte" || price != 320 || catName != "Coffee" {
		t.Fatalf("Latte parsed wrong: name=%q price=%d category=%q", name, price, catName)
	}
	var barcodeCount int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM item_barcodes WHERE item_id = (SELECT id FROM items WHERE sku = '20001')`).Scan(&barcodeCount); err != nil {
		t.Fatalf("count barcodes: %v", err)
	}
	if barcodeCount != 0 {
		t.Fatalf("bkp import must never populate a barcode from ProductNumber, got %d", barcodeCount)
	}

	// The deleted (Status=3) row must not have been created.
	var deletedCount int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM items WHERE sku = '20002'`).Scan(&deletedCount); err != nil {
		t.Fatalf("count deleted row: %v", err)
	}
	if deletedCount != 0 {
		t.Fatalf("Status=3 row must be excluded, found %d items", deletedCount)
	}
}

// TestImport_CSVUploadStillWorks is a regression guard for the auto-detect
// wiring: a plain CSV upload (no ZIP magic) must still route through
// catimport.Parse exactly as before.
func TestImport_CSVUploadStillWorks(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	body, ct := multipartCSV(t, importCSV, map[string]string{"commit": "1"})
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("commit: code %d body %s", rec.Code, rec.Body.String())
	}
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM items WHERE sku = 'W1'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("CSV upload should still import Widget (n=%d err=%v)", n, err)
	}
}

// TestImport_BkpUpload_InvalidBackupShowsGenericMessage covers the
// error-translation path: an upload that sniffs as a ZIP but isn't a valid
// .bkp (missing backup.db/meta.inf) must show a translated, generic
// message — never raw Go error text — on the operator's screen.
func TestImport_BkpUpload_InvalidBackupShowsGenericMessage(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	initAuthTestI18n(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	// A ZIP with neither backup.db nor meta.inf inside.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("something-else.txt")
	if err != nil {
		t.Fatalf("create zip entry: %v", err)
	}
	if _, err := w.Write([]byte("not a backup")); err != nil {
		t.Fatalf("write zip entry: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}

	body, ct := multipartFile(t, "notabackup.bkp", buf.Bytes(), nil)
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rec.Code)
	}
	resp := rec.Body.String()
	if strings.Contains(resp, "backup.db") || strings.Contains(resp, "meta.inf") {
		t.Fatalf("raw internal file names must not leak to the operator, got: %s", resp)
	}
	if !strings.Contains(resp, "recognised backup file") {
		t.Fatalf("should show the translated bkp_unrecognised message, got: %s", resp)
	}
}

// TestImport_BkpUploadCarriesTaxColumnsThrough is ut-docs#512's real
// motivating case: the speedy kasse Products table's TaxPercentage/
// TaxPercentage2 columns are the actual source the issue's own café
// conversion described — this proves the .bkp path (not just the CSV path)
// groups items onto tax codes by pair and populates ut-plugin-tax-de's
// takeaway_rate_overrides, exactly like TestImport_TaxColumnsGroupOntoTax-
// CodesAndPopulateOverrides already proves for CSV.
func TestImport_BkpUploadCarriesTaxColumnsThrough(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	installTaxDePlugin(t, dp.Db)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	zipBytes := buildBkpZipForPagesTestWithTaxRows(t, []bkpTaxRow{
		{ProductNumber: "30001", Name: "Cappuccino", Category: "Coffee", Price: 3.50, TaxPct: 19.0, TaxPct2: 7.0},
		{ProductNumber: "30002", Name: "Espresso", Category: "Coffee", Price: 2.20, TaxPct: 19.0, TaxPct2: 19.0},
	})
	body, ct := multipartFile(t, "Backup 2026-08-09.bkp", zipBytes, map[string]string{"commit": "1"})
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("commit: code %d body %s", rec.Code, rec.Body.String())
	}

	cappCode, cappRate, cappTA := itemTaxPair(t, dp.Db, "30001")
	if cappRate != 1900 || cappTA == nil || *cappTA != 700 {
		t.Fatalf("Cappuccino pair = (%d,%v), want (1900,&700)", cappRate, cappTA)
	}
	espCode, espRate, espTA := itemTaxPair(t, dp.Db, "30002")
	if espRate != 1900 || espTA != nil {
		t.Fatalf("Espresso pair = (%d,%v), want (1900,nil) — equal pair needs no override", espRate, espTA)
	}
	if cappCode == espCode {
		t.Fatal("the two distinct 19%% groups must not share a tax code, even from the .bkp path")
	}

	var overridesJSON string
	if err := dp.Db.QueryRow(
		`SELECT value_json FROM plugin_settings WHERE plugin_id = 'com.universaltill.tax-de' AND key = 'takeaway_rate_overrides'`).
		Scan(&overridesJSON); err != nil {
		t.Fatalf("takeaway_rate_overrides not written from a .bkp import: %v", err)
	}
	if !strings.Contains(overridesJSON, cappCode) {
		t.Fatalf("overrides JSON %s should contain Cappuccino's tax code %s", overridesJSON, cappCode)
	}
}
