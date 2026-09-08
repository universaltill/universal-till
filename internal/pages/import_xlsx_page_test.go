package pages

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/httpx"
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

// TestImport_XLSXUnconfirmedCurrencyGatedAndReparses is the xlsx mirror of
// TestImport_UnconfirmedCurrency_BkpPathGatedAndReparses (review finding,
// ut-docs#1837 — the same coverage gap F10 closed for .bkp on ut-docs#970).
// The currency-confirm branch re-parses the ORIGINAL upload bytes under the
// confirmed currency's decimal count, and this card added a third case to
// that switch; nothing exercised it. It is also the one place ParseXLSX is
// called after the upload's own Seek position has been moved (the hash, the
// sniff, the first parse), so it proves ParseXLSX's io.ReaderAt access is
// genuinely offset-independent rather than accidentally working. 5.00 is
// 500 minor units under GBP (2 decimals) and 5 under IRT (0), so a stale
// first parse is observable.
func TestImport_XLSXUnconfirmedCurrencyGatedAndReparses(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	t.Cleanup(func() { httpx.InitCurrency("GBP") })
	dp := newImportTestDepsWithCurrencyState(t, false)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	xlsxBytes := buildXLSXForPagesTest(t, [][]string{
		{"Name", "SKU", "Price", "Category"},
		{"Latte Macchiato", "LM1", "5.00", "Getränke"},
	})

	// First attempt: unconfirmed currency must prompt, not import.
	body, ct := multipartFile(t, "catalog.xlsx", xlsxBytes, map[string]string{"commit": "1"})
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unconfirmed xlsx commit: code %d body %s", rec.Code, rec.Body.String())
	}
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM items WHERE sku = 'LM1'`).Scan(&n); err != nil {
		t.Fatalf("count items: %v", err)
	}
	if n != 0 {
		t.Fatalf("unconfirmed xlsx commit wrote %d rows, want 0 (it must prompt first)", n)
	}

	// Confirm as IRT (0 decimals, unlike GBP's 2).
	body2, ct2 := multipartFile(t, "catalog.xlsx", xlsxBytes, map[string]string{"commit": "1", "confirm_currency": "IRT"})
	req2 := httptest.NewRequest(http.MethodPost, "/api/import", body2)
	req2.Header.Set("Content-Type", ct2)
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("confirmed xlsx commit: code %d body %s", rec2.Code, rec2.Body.String())
	}
	var priceMinor int64
	if err := dp.Db.QueryRow(`SELECT base_price FROM items WHERE sku = 'LM1'`).Scan(&priceMinor); err != nil {
		t.Fatalf("item not created: %v (body: %s)", err, rec2.Body.String())
	}
	if priceMinor != 5 {
		t.Fatalf("xlsx price = %d minor units, want 5 (IRT, 0 decimals — proves the workbook bytes were re-parsed under the confirmed currency, not just relabeled)", priceMinor)
	}
}

// TestImport_BkpStillRoutesToBkpParserAfterXLSXSniff guards the rewiring
// this card did to the format branch (review finding, ut-docs#1837): isBkp
// stopped being "the upload is a ZIP" and became "a ZIP that LooksLikeXLSXZip
// rejected". A .bkp backup is a plain ZIP with the identical magic bytes, so
// a sniff that got too eager would silently send the pilot's own till backup
// through the workbook parser and fail an import that works today.
func TestImport_BkpStillRoutesToBkpParserAfterXLSXSniff(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	body, ct := multipartFile(t, "Backup 2026-08-09.bkp", buildBkpZipForPagesTest(t), nil)
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("preview: code %d body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Latte") {
		t.Fatalf(".bkp must still route to ParseBkp after the xlsx sniff, got: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "worksheet") {
		t.Fatalf(".bkp preview must not claim a worksheet was read, got: %s", rec.Body.String())
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
