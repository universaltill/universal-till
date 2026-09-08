package catimport

import (
	"bytes"
	"errors"
	"testing"

	"github.com/xuri/excelize/v2"
)

// buildXLSX writes rows (headers first) into a fresh in-memory workbook's
// default sheet ("Sheet1") and returns the encoded bytes. Test helper only.
func buildXLSX(t *testing.T, rows [][]string) []byte {
	t.Helper()
	return buildXLSXSheet(t, "Sheet1", rows)
}

func buildXLSXSheet(t *testing.T, sheet string, rows [][]string) []byte {
	t.Helper()
	f := excelize.NewFile()
	defer f.Close()
	if sheet != "Sheet1" {
		if _, err := f.NewSheet(sheet); err != nil {
			t.Fatalf("NewSheet: %v", err)
		}
		f.SetActiveSheet(0)
	}
	for r, row := range rows {
		for c, v := range row {
			cell, err := excelize.CoordinatesToCellName(c+1, r+1)
			if err != nil {
				t.Fatalf("CoordinatesToCellName: %v", err)
			}
			if err := f.SetCellStr(sheet, cell, v); err != nil {
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

func TestParseXLSX_Basic(t *testing.T) {
	data := buildXLSX(t, [][]string{
		{"Name", "SKU", "Price", "Barcode", "Category"},
		{"Coca-Cola 330ml", "10001", "1.40", "5449000000996", "Drinks"},
		{"Bananas", "10002", "0.89", "", "Produce"},
	})
	res, err := ParseXLSX(bytes.NewReader(data), int64(len(data)), 2, testEnabledIDs, false)
	if err != nil {
		t.Fatalf("ParseXLSX: %v", err)
	}
	if res.SheetName != "Sheet1" {
		t.Errorf("SheetName = %q, want Sheet1", res.SheetName)
	}
	if len(res.Items) != 2 {
		t.Fatalf("got %d items, want 2: %+v", len(res.Items), res.Items)
	}
	if res.Items[0].Name != "Coca-Cola 330ml" || res.Items[0].PriceMinor != 140 {
		t.Errorf("row0 = %+v, want Coca-Cola 330ml @ 140", res.Items[0])
	}
	if res.Items[0].Barcode == "" {
		t.Errorf("row0 barcode should have matched the EAN13 registry entry, got empty")
	}
	if res.Items[1].Name != "Bananas" || res.Items[1].PriceMinor != 89 {
		t.Errorf("row1 = %+v, want Bananas @ 89", res.Items[1])
	}
}

// TestParseXLSX_ReusesDetectFormat: the same Square-shaped header row that
// squareCSV's own test relies on must classify identically whether it
// arrives as CSV or as a workbook (ut-docs#1837 AC1) — this is what
// actually proves ParseXLSX shares DetectFormat/headerIndex with Parse,
// not just similar-looking code.
func TestParseXLSX_ReusesDetectFormat(t *testing.T) {
	data := buildXLSX(t, [][]string{
		{"Token", "Item Name", "Variation Name", "SKU", "Description", "Category", "Price", "GTIN"},
		{"tok1", "Coffee", "Large", "SQ-1", "Fresh brew", "Hot Drinks", "3.50", ""},
	})
	res, err := ParseXLSX(bytes.NewReader(data), int64(len(data)), 2, testEnabledIDs, false)
	if err != nil {
		t.Fatalf("ParseXLSX: %v", err)
	}
	if res.Format != "square" {
		t.Errorf("Format = %q, want square (same detection Parse's own squareCSV test expects)", res.Format)
	}
	// The Square variation-name special case (qualifying the name with a
	// non-"Regular" variation) must also carry over.
	if res.Items[0].Name != "Coffee Large" {
		t.Errorf("Name = %q, want %q (variation-name qualified)", res.Items[0].Name, "Coffee Large")
	}
}

// TestParseXLSX_GermanLocaleTextPrice: AC3's sharp edge — a price cell the
// source stored as German-formatted TEXT ("1.234,56") must resolve to
// 123456 minor units (€1,234.56), not silently misread as 1.234. Feeds a
// real value through the string cell path (SetCellStr, matching how a
// third-party export with text-formatted price cells would arrive), so
// this exercises ParsePrice's existing normalizeDecimalComma exactly as a
// CSV cell would.
func TestParseXLSX_GermanLocaleTextPrice(t *testing.T) {
	data := buildXLSX(t, [][]string{
		{"Name", "Price"},
		{"Espressomaschine", "1.234,56"},
	})
	res, err := ParseXLSX(bytes.NewReader(data), int64(len(data)), 2, testEnabledIDs, false)
	if err != nil {
		t.Fatalf("ParseXLSX: %v", err)
	}
	if len(res.Items) != 1 || res.Items[0].PriceMinor != 123456 {
		t.Fatalf("got %+v, want PriceMinor=123456 (a German-formatted €1.234,56 cell)", res.Items)
	}
}

// TestParseXLSX_GermanLocaleNumericPrice: the more common real case — a
// genuinely-numeric Excel cell (not text) always round-trips through
// GetRows as plain dot-decimal regardless of the workbook's display
// locale, so this must parse identically to the text case above.
func TestParseXLSX_GermanLocaleNumericPrice(t *testing.T) {
	f := excelize.NewFile()
	defer f.Close()
	if err := f.SetCellStr("Sheet1", "A1", "Name"); err != nil {
		t.Fatal(err)
	}
	if err := f.SetCellStr("Sheet1", "B1", "Price"); err != nil {
		t.Fatal(err)
	}
	if err := f.SetCellStr("Sheet1", "A2", "Espressomaschine"); err != nil {
		t.Fatal(err)
	}
	if err := f.SetCellValue("Sheet1", "B2", 1234.56); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := f.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	res, err := ParseXLSX(bytes.NewReader(buf.Bytes()), int64(buf.Len()), 2, testEnabledIDs, false)
	if err != nil {
		t.Fatalf("ParseXLSX: %v", err)
	}
	if len(res.Items) != 1 || res.Items[0].PriceMinor != 123456 {
		t.Fatalf("got %+v, want PriceMinor=123456", res.Items)
	}
}

// TestParseXLSX_FirstSheetOnly: AC2 — a multi-sheet workbook reads only
// the first sheet and reports which one via Result.SheetName. The second
// sheet's row ("SHOULD NOT IMPORT") must never appear in Items.
func TestParseXLSX_FirstSheetOnly(t *testing.T) {
	f := excelize.NewFile()
	defer f.Close()
	// Sheet1 (index 0, created by NewFile) is the data sheet.
	if err := f.SetSheetName("Sheet1", "Products"); err != nil {
		t.Fatal(err)
	}
	for cell, v := range map[string]string{"A1": "Name", "B1": "Price", "A2": "Widget", "B2": "2.00"} {
		if err := f.SetCellStr("Products", cell, v); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.NewSheet("Notes"); err != nil {
		t.Fatal(err)
	}
	if err := f.SetCellStr("Notes", "A1", "SHOULD NOT IMPORT"); err != nil {
		t.Fatal(err)
	}
	f.SetActiveSheet(0)
	var buf bytes.Buffer
	if _, err := f.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	res, err := ParseXLSX(bytes.NewReader(buf.Bytes()), int64(buf.Len()), 2, testEnabledIDs, false)
	if err != nil {
		t.Fatalf("ParseXLSX: %v", err)
	}
	if res.SheetName != "Products" {
		t.Errorf("SheetName = %q, want Products", res.SheetName)
	}
	for _, it := range res.Items {
		if it.Name == "SHOULD NOT IMPORT" {
			t.Errorf("second sheet's row leaked into Items: %+v", res.Items)
		}
	}
	if len(res.Items) != 1 || res.Items[0].Name != "Widget" {
		t.Errorf("got %+v, want just Widget", res.Items)
	}
}

// TestParseXLSX_MergedCells: AC4 — reject, never guess.
func TestParseXLSX_MergedCells(t *testing.T) {
	f := excelize.NewFile()
	defer f.Close()
	for cell, v := range map[string]string{"A1": "Name", "B1": "Price", "A2": "Widget", "B2": "2.00"} {
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
	_, err := ParseXLSX(bytes.NewReader(buf.Bytes()), int64(buf.Len()), 2, testEnabledIDs, false)
	if !errors.Is(err, ErrXLSXMergedCells) {
		t.Errorf("err = %v, want ErrXLSXMergedCells", err)
	}
}

// TestParseXLSX_LeadingTitleRow: a title row that doesn't match any known
// header synonym rejects the whole file with ErrNoNameColumn — same
// "reject, never guess" outcome a malformed CSV header gets (AC4).
func TestParseXLSX_LeadingTitleRow(t *testing.T) {
	data := buildXLSX(t, [][]string{
		{"Catalog Export - Cafe Example - 2026-09-08"},
		{"Name", "Price"},
		{"Widget", "2.00"},
	})
	_, err := ParseXLSX(bytes.NewReader(data), int64(len(data)), 2, testEnabledIDs, false)
	if !errors.Is(err, ErrNoNameColumn) {
		t.Errorf("err = %v, want ErrNoNameColumn (a title-row-as-header must reject, never guess which row is the real header)", err)
	}
}

// TestParseXLSX_TrailingTotalsRow: a totals row with no name column value
// must be REPORTED as a per-row issue, never silently imported as a real
// catalog item (AC4) — same mechanism a malformed CSV row already gets.
func TestParseXLSX_TrailingTotalsRow(t *testing.T) {
	data := buildXLSX(t, [][]string{
		{"Name", "Price"},
		{"Widget", "2.00"},
		{"", "127.50"}, // a totals row: no name, a grand-total "price"
	})
	res, err := ParseXLSX(bytes.NewReader(data), int64(len(data)), 2, testEnabledIDs, false)
	if err != nil {
		t.Fatalf("ParseXLSX: %v", err)
	}
	if len(res.Items) != 2 {
		t.Fatalf("got %d items, want 2 (including the flagged totals row)", len(res.Items))
	}
	if res.Items[1].Issue != IssueMissingName {
		t.Errorf("totals row Issue = %q, want %q — a totals row must be reported, never silently imported as a real item", res.Items[1].Issue, IssueMissingName)
	}
}

func TestParseXLSX_NoNameColumn(t *testing.T) {
	data := buildXLSX(t, [][]string{
		{"Foo", "Bar"},
		{"1", "2"},
	})
	_, err := ParseXLSX(bytes.NewReader(data), int64(len(data)), 2, testEnabledIDs, false)
	if !errors.Is(err, ErrNoNameColumn) {
		t.Errorf("err = %v, want ErrNoNameColumn", err)
	}
}

func TestLooksLikeLegacyXLS(t *testing.T) {
	xlsHeader := []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1, 0, 0}
	if !LooksLikeLegacyXLS(xlsHeader) {
		t.Errorf("LooksLikeLegacyXLS(OLE2 header) = false, want true")
	}
	csvHeader := []byte("Name,SKU,Price\n")
	if LooksLikeLegacyXLS(csvHeader) {
		t.Errorf("LooksLikeLegacyXLS(CSV header) = true, want false")
	}
}

func TestLooksLikeXLSXZip(t *testing.T) {
	data := buildXLSX(t, [][]string{{"Name", "Price"}, {"Widget", "2.00"}})
	if !LooksLikeXLSXZip(bytes.NewReader(data), int64(len(data))) {
		t.Errorf("LooksLikeXLSXZip(real xlsx) = false, want true")
	}
	// A plain, non-OOXML ZIP (no [Content_Types].xml/xl/workbook.xml) —
	// same shape a .bkp archive or any other unrelated ZIP would have —
	// must NOT be mistaken for a workbook.
	notXLSX := []byte("PK\x05\x06" + string(make([]byte, 18))) // empty-zip EOCD record
	if LooksLikeXLSXZip(bytes.NewReader(notXLSX), int64(len(notXLSX))) {
		t.Errorf("LooksLikeXLSXZip(empty non-xlsx zip) = true, want false")
	}
}
