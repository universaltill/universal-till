package catimport

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/xuri/excelize/v2"
)

// buildXLSX writes rows (headers first) into a fresh in-memory workbook's
// default sheet ("Sheet1") and returns the encoded bytes. Test helper only —
// every multi-sheet test (e.g. TestParseXLSX_FirstSheetOnly) builds its
// workbook directly with excelize instead, since this helper only ever
// needs to write to the one default sheet.
func buildXLSX(t *testing.T, rows [][]string) []byte {
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

// TestParseXLSX_RoundingDisplayFormatDoesNotChangePrice (review finding,
// ut-docs#1837): GetRows APPLIES a cell's number format, so a numeric price
// cell carrying a zero-decimal display format ("#,##0" or "0" — a merchant
// formatting the column as a plain Number, or an export tool writing one)
// renders as "1" for a stored 1.4. Reading that would have priced a €1.40
// item at €1.00 on the operator's real catalog with no warning at all — the
// same silent money-corruption class ut-docs#586 exists to prevent, just
// arriving through a spreadsheet instead of a CSV. The numeric columns are
// therefore read from a raw, unformatted pass. Pinned per-format so a
// regression names the format that broke.
func TestParseXLSX_RoundingDisplayFormatDoesNotChangePrice(t *testing.T) {
	for _, tc := range []struct {
		name   string
		numFmt string
		stored float64
		want   int64
	}{
		{"zero-decimal number format", "#,##0", 1.4, 140},
		{"bare zero format", "0", 1.4, 140},
		{"rounds up when displayed", "0", 0.89, 89},
		{"grouped two-decimal", "#,##0.00", 1234.56, 123456},
		{"currency suffix", `#,##0.00\ "€"`, 1234.56, 123456},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
			if err := f.SetCellValue("Sheet1", "B2", tc.stored); err != nil {
				t.Fatal(err)
			}
			numFmt := tc.numFmt
			st, err := f.NewStyle(&excelize.Style{CustomNumFmt: &numFmt})
			if err != nil {
				t.Fatal(err)
			}
			if err := f.SetCellStyle("Sheet1", "B2", "B2", st); err != nil {
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
			if len(res.Items) != 1 || res.Items[0].PriceMinor != tc.want {
				t.Fatalf("got %+v, want PriceMinor=%d — the cell's DISPLAY format must never change the imported price", res.Items, tc.want)
			}
		})
	}
}

// TestParseXLSX_PercentFormattedTaxCell is the other half of the finding
// above: the tax columns must KEEP the formatted value, because a
// percent-formatted cell stores 0.19 and reading that raw would parse
// "successfully" as a 0.19% rate — silently wrong on a compliance-sensitive
// field (ut-docs#512), where a wrong price at least stays visible on the
// preview grid.
func TestParseXLSX_PercentFormattedTaxCell(t *testing.T) {
	f := excelize.NewFile()
	defer f.Close()
	for cell, v := range map[string]string{"A1": "Name", "B1": "Price", "C1": "Tax rate", "A2": "Widget", "B2": "2.00"} {
		if err := f.SetCellStr("Sheet1", cell, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.SetCellValue("Sheet1", "C2", 0.19); err != nil {
		t.Fatal(err)
	}
	pct := "0%"
	st, err := f.NewStyle(&excelize.Style{CustomNumFmt: &pct})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.SetCellStyle("Sheet1", "C2", "C2", st); err != nil {
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
	if len(res.Items) != 1 || !res.Items[0].HasTax || res.Items[0].TaxRateBP != 1900 {
		t.Fatalf("got %+v, want a 1900bp (19%%) rate from a percent-formatted cell", res.Items)
	}
}

// TestParseXLSX_BlankSeparatorRowSkipped (review finding, ut-docs#1837):
// encoding/csv drops a blank line, so Parse never produces a row for one;
// GetRows hands back an empty record instead, which used to become a
// phantom "missing name and bad price" row on the preview grid for a
// spreadsheet that a CSV of the same export imported cleanly (AC1).
func TestParseXLSX_BlankSeparatorRowSkipped(t *testing.T) {
	f := excelize.NewFile()
	defer f.Close()
	// Row 3 is left entirely empty between the two data rows.
	for cell, v := range map[string]string{"A1": "Name", "B1": "Price", "A2": "Widget", "B2": "2.00", "A4": "Gadget", "B4": "3.00"} {
		if err := f.SetCellStr("Sheet1", cell, v); err != nil {
			t.Fatal(err)
		}
	}
	var buf bytes.Buffer
	if _, err := f.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	res, err := ParseXLSX(bytes.NewReader(buf.Bytes()), int64(buf.Len()), 2, testEnabledIDs, false)
	if err != nil {
		t.Fatalf("ParseXLSX: %v", err)
	}
	if len(res.Items) != 2 {
		t.Fatalf("got %d items %+v, want 2 — a blank separator row is not a problem row", len(res.Items), res.Items)
	}
	// And byte-for-byte the same outcome the CSV of that export gives.
	csvRes, err := Parse(bytes.NewReader([]byte("Name,Price\nWidget,2.00\n\nGadget,3.00\n")), 2, testEnabledIDs, false)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(csvRes.Items) != len(res.Items) {
		t.Fatalf("xlsx produced %d items, the same export as CSV produced %d (AC1: they must agree)", len(res.Items), len(csvRes.Items))
	}
	for i := range res.Items {
		if res.Items[i].Name != csvRes.Items[i].Name || res.Items[i].PriceMinor != csvRes.Items[i].PriceMinor || res.Items[i].Issue != csvRes.Items[i].Issue {
			t.Errorf("row %d differs: xlsx=%+v csv=%+v", i, res.Items[i], csvRes.Items[i])
		}
	}
}

// TestParseXLSX_DecompressionBombRejected (review finding, ut-docs#1837):
// excelize's default UnzipSizeLimit is 16GB and ReadZipReader buffers a
// non-worksheet member whole in RAM, so a small upload declaring a huge
// member would be allocated in full — an OOM kill of the whole POS process
// (checkout included) on the Pi-class hardware this ships on, from one
// upload. Both the sniff and the parse must bound it.
func TestParseXLSX_DecompressionBombRejected(t *testing.T) {
	base := buildXLSX(t, [][]string{{"Name", "Price"}, {"Widget", "2.00"}})
	zr, err := zip.NewReader(bytes.NewReader(base), int64(len(base)))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	for _, fh := range zr.File {
		rc, oerr := fh.Open()
		if oerr != nil {
			t.Fatal(oerr)
		}
		w, cerr := zw.Create(fh.Name)
		if cerr != nil {
			t.Fatal(cerr)
		}
		if _, err := io.Copy(w, rc); err != nil {
			t.Fatal(err)
		}
		_ = rc.Close()
	}
	w, err := zw.Create("xl/bomb.xml")
	if err != nil {
		t.Fatal(err)
	}
	chunk := make([]byte, 1<<20) // compresses to almost nothing
	for written := 0; written <= xlsxMaxUnzipSize; written += len(chunk) {
		if _, err := w.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	b := out.Bytes()
	if int64(len(b)) > xlsxMaxUnzipSize/8 {
		t.Fatalf("the bomb fixture (%d bytes) must stay far smaller than the limit it trips, or it proves nothing", len(b))
	}
	if LooksLikeXLSXZip(bytes.NewReader(b), int64(len(b))) {
		t.Errorf("the sniff accepted a workbook declaring more than %d bytes unzipped", xlsxMaxUnzipSize)
	}
	if _, err := ParseXLSX(bytes.NewReader(b), int64(len(b)), 2, testEnabledIDs, false); err == nil {
		t.Errorf("ParseXLSX accepted a workbook declaring more than %d bytes unzipped", xlsxMaxUnzipSize)
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
