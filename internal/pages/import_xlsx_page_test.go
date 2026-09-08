package pages

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

// buildXLSXForPagesTest writes rows (headers first) into a fresh in-memory
// workbook's default sheet and returns the encoded bytes — the pages-layer
// counterpart of catimport's own buildXLSX test helper (kept separate:
// internal test helpers aren't exported across packages).
func buildXLSXForPagesTest(t *testing.T, rows [][]string) []byte {
	t.Helper()
	f := excelize.NewFile()
	defer f.Close()
	for r, row := range rows {
		for c, v := range row {
			cell, err := excelize.CoordinatesToCellName(c+1, r+1)
			if err != nil {
				t.Fatalf("CoordinatesToCellName: %v", err)
			}
			if err := f.SetCellStr("Sheet1", cell, v); err != nil {
				t.Fatalf("SetCellStr: %v", err)
			}
		}
	}
	var buf bytes.Buffer
	if _, err := f.WriteTo(&buf); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	return buf.Bytes()
}

// TestImport_AutoDetectsXLSXUpload_Preview covers the auto-detect wiring
// this card adds (ut-docs#1837): an .xlsx-shaped upload — a ZIP container,
// same magic bytes as a .bkp backup — must be routed through
// catimport.ParseXLSX, not catimport.ParseBkp (which would fail on it:
// ErrBkpMissingFiles, no backup.db/meta.inf) and not catimport.Parse
// (which would fail to read binary zip bytes as CSV).
func TestImport_AutoDetectsXLSXUpload_Preview(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	xlsxBytes := buildXLSXForPagesTest(t, [][]string{
		{"Name", "SKU", "Price", "Category"},
		{"Widget", "W1", "1.50", "Snacks"},
	})
	body, ct := multipartFile(t, "catalog.xlsx", xlsxBytes, nil) // no commit
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("preview: code %d body %s", rec.Code, rec.Body.String())
	}
	resp := rec.Body.String()
	if !strings.Contains(resp, "Widget") {
		t.Fatalf("preview should list the parsed xlsx row, got: %s", resp)
	}
	if !strings.Contains(resp, "Sheet1") {
		t.Fatalf("preview should name which worksheet was read (AC2), got: %s", resp)
	}
}

// TestImport_AutoDetectsXLSXUpload_Commit covers the full commit path for
// an .xlsx upload actually creating the item.
func TestImport_AutoDetectsXLSXUpload_Commit(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	xlsxBytes := buildXLSXForPagesTest(t, [][]string{
		{"Name", "SKU", "Price", "Category"},
		{"Widget", "W1", "1.50", "Snacks"},
	})
	body, ct := multipartFile(t, "catalog.xlsx", xlsxBytes, map[string]string{"commit": "1"})
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("commit: code %d body %s", rec.Code, rec.Body.String())
	}
	var name string
	var price int64
	if err := dp.Db.QueryRow(`SELECT name, base_price FROM items WHERE sku = 'W1'`).Scan(&name, &price); err != nil {
		t.Fatalf("Widget not created: %v", err)
	}
	if name != "Widget" || price != 150 {
		t.Fatalf("Widget parsed wrong: name=%q price=%d", name, price)
	}
}

// TestImport_XLSXMergedCellsRejected: AC4 — a workbook using merged cells
// is rejected with a specific message, never silently misimported.
func TestImport_XLSXMergedCellsRejected(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	f := excelize.NewFile()
	defer f.Close()
	for cell, v := range map[string]string{"A1": "Name", "B1": "Price", "A2": "Widget", "B2": "1.50"} {
		if err := f.SetCellStr("Sheet1", cell, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.MergeCell("Sheet1", "A1", "B1"); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := f.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}

	body, ct := multipartFile(t, "catalog.xlsx", buf.Bytes(), nil)
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "merged cells") {
		t.Fatalf("body should show the specific merged-cells message, got: %s", rec.Body.String())
	}
}

// TestImport_LegacyXLSRejectedWithSpecificMessage: AC6 — a legacy binary
// .xls upload (OLE2 container, not a ZIP at all) gets its own specific
// "save as .xlsx or CSV" message, never the generic invalid_file one.
func TestImport_LegacyXLSRejectedWithSpecificMessage(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	fakeOLE2 := append([]byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}, make([]byte, 64)...)
	body, ct := multipartFile(t, "old.xls", fakeOLE2, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), ".xlsx") || !strings.Contains(rec.Body.String(), "CSV") {
		t.Fatalf("body should tell the operator to save as .xlsx or CSV, got: %s", rec.Body.String())
	}
}
