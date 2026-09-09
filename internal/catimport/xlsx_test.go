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

// TestParseXLSX_MergeInsideDataRowStillRejects: AC4/ut-docs#1853 — the
// narrowed rectangle check must still catch a merge that isn't in the
// header row itself, as long as it's inside a recognised data column of
// the contiguous data block. Same "silently shift a real value" risk
// TestParseXLSX_MergedCells covers for the header row, one row down.
func TestParseXLSX_MergeInsideDataRowStillRejects(t *testing.T) {
	f := excelize.NewFile()
	defer f.Close()
	for cell, v := range map[string]string{
		"A1": "Name", "B1": "Price", "C1": "Category",
		"A2": "Widget", "B2": "2.00", "C2": "Misc",
	} {
		if err := f.SetCellStr("Sheet1", cell, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.MergeCell("Sheet1", "B2", "C2"); err != nil { // Price+Category merged on the data row
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := f.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	_, err := ParseXLSX(bytes.NewReader(buf.Bytes()), int64(buf.Len()), 2, testEnabledIDs, false)
	if !errors.Is(err, ErrXLSXMergedCells) {
		t.Errorf("err = %v, want ErrXLSXMergedCells (merge overlaps a recognised column on a data row, not just the header)", err)
	}
}

// TestParseXLSX_DecorativeMergeOutsideDataColumnsImportsSuccessfully:
// ut-docs#1853 — a merge sharing the header row but sitting in a column
// headerIndex never recognised (a company-logo cell off to the side of
// the real "name"/"price" columns) cannot shift a value ParseXLSX ever
// reads, so it must not block the import.
func TestParseXLSX_DecorativeMergeOutsideDataColumnsImportsSuccessfully(t *testing.T) {
	f := excelize.NewFile()
	defer f.Close()
	for cell, v := range map[string]string{
		"A1": "Name", "B1": "Price", "D1": "Acme Wholesale Ltd",
		"A2": "Widget", "B2": "2.00",
	} {
		if err := f.SetCellStr("Sheet1", cell, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.MergeCell("Sheet1", "D1", "F1"); err != nil { // decorative, columns D-F: outside name(A)/price(B)
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := f.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	res, err := ParseXLSX(bytes.NewReader(buf.Bytes()), int64(buf.Len()), 2, testEnabledIDs, false)
	if err != nil {
		t.Fatalf("ParseXLSX: %v, want success (merge is outside the recognised name/price columns)", err)
	}
	if len(res.Items) != 1 || res.Items[0].Name != "Widget" || res.Items[0].PriceMinor != 200 {
		t.Fatalf("got %+v, want just Widget @ 200", res.Items)
	}
}

// TestParseXLSX_MergedNoteFarBelowDataImportsSuccessfully: ut-docs#1853 —
// a merged note or trailing-annotation row separated from the real data
// by a blank gap is outside the contiguous block ParseXLSX's loop treats
// as data, so it gets the same "report, don't reject" handling any
// malformed trailing CSV row would (TestParseXLSX_TrailingTotalsRow),
// not a whole-file rejection.
func TestParseXLSX_MergedNoteFarBelowDataImportsSuccessfully(t *testing.T) {
	f := excelize.NewFile()
	defer f.Close()
	for cell, v := range map[string]string{
		"A1": "Name", "B1": "Price",
		"A2": "Widget", "B2": "2.00",
		"A40": "Reconciled against supplier invoice #4471 — see binder", // the note, 38 rows below
	} {
		if err := f.SetCellStr("Sheet1", cell, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.MergeCell("Sheet1", "A40", "F40"); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := f.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	res, err := ParseXLSX(bytes.NewReader(buf.Bytes()), int64(buf.Len()), 2, testEnabledIDs, false)
	if err != nil {
		t.Fatalf("ParseXLSX: %v, want success (the merge is far below the real data, separated by a blank gap)", err)
	}
	found := false
	for _, it := range res.Items {
		if it.Name == "Widget" && it.PriceMinor == 200 {
			found = true
		}
	}
	if !found {
		t.Errorf("got %+v, want the real Widget row imported (the far-below note row may also appear, flagged, same as any trailing CSV row)", res.Items)
	}
}

// TestParseXLSX_MergeAfterBlankSeparatorRowStillRejects: ut-docs#1853,
// review finding 1 — the blocker a first draft of this fix shipped. A
// blank separator row (TestParseXLSX_BlankSeparatorRowSkipped) does not
// end the "real data" block: the import loop only `continue`s on it, it
// keeps scanning every row after. A merge on a clean row past the gap
// must still reject — otherwise it silently blanks a field (here, the
// tax rate — no Issue is raised for a blanked tax cell, unlike a blanked
// name/price) with the whole import reporting success.
func TestParseXLSX_MergeAfterBlankSeparatorRowStillRejects(t *testing.T) {
	f := excelize.NewFile()
	defer f.Close()
	for cell, v := range map[string]string{
		"A1": "Name", "B1": "Price", "C1": "Tax rate",
		"A2": "Cola", "B2": "1.40", "C2": "19%",
		// row 3 left entirely blank — a section separator
		"A4": "Bananas", "B4": "0.89", "C4": "7%",
		"A5": "Bread", "B5": "1.20", "C5": "7%",
	} {
		if err := f.SetCellStr("Sheet1", cell, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.MergeCell("Sheet1", "B5", "C5"); err != nil { // Price+Tax merged on a clean row past the gap
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := f.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	_, err := ParseXLSX(bytes.NewReader(buf.Bytes()), int64(buf.Len()), 2, testEnabledIDs, false)
	if !errors.Is(err, ErrXLSXMergedCells) {
		t.Errorf("err = %v, want ErrXLSXMergedCells (merge is on a real item row resumed after a blank separator, not on a trailing note)", err)
	}
}

// TestParseXLSX_MergeOnLastCleanDataRowRejects: boundary case for
// dataRectangleLastRow — a merge on exactly the last row that parses as
// a clean item (not one row before, not one row after) must still
// reject. Pins the off-by-one this helper could easily regress into.
// Deliberately merges Price+Tax rather than Name+Price: Excel keeps a
// merge's value only in its top-left cell and blanks the rest, so a
// merge starting AT the price cell (B, top-left here) leaves the row's
// own name/price intact — it's the row's Tax cell (C, not top-left)
// that goes silently blank, which is the actual risk this guard exists
// for. A Name+Price merge would instead blank the price and make the
// row fail its own "clean item" check — a different, already-accepted
// case (see the doc comment on dataRectangleLastRow), not this boundary.
func TestParseXLSX_MergeOnLastCleanDataRowRejects(t *testing.T) {
	f := excelize.NewFile()
	defer f.Close()
	for cell, v := range map[string]string{
		"A1": "Name", "B1": "Price", "C1": "Tax rate",
		"A2": "Widget", "B2": "2.00", "C2": "19%",
		"A3": "Gadget", "B3": "3.00", "C3": "19%", // the last clean row
	} {
		if err := f.SetCellStr("Sheet1", cell, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.MergeCell("Sheet1", "B3", "C3"); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := f.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	_, err := ParseXLSX(bytes.NewReader(buf.Bytes()), int64(buf.Len()), 2, testEnabledIDs, false)
	if !errors.Is(err, ErrXLSXMergedCells) {
		t.Errorf("err = %v, want ErrXLSXMergedCells (merge is on the LAST clean data row, still inside the rectangle)", err)
	}
}

// TestParseXLSX_OnlyOneOfSeveralMergesOverlappingStillRejects: several
// merges, only one of which actually overlaps the rectangle — the loop
// in rejectMergedCells must not stop checking (or short-circuit wrongly)
// just because an earlier, harmless merge didn't overlap. Same
// Price+Category (not Name+Price) construction as the boundary test
// above, and for the same reason: the corrupting merge must not blank
// the row's own name/price, or the row stops counting as "clean" and
// this test would accidentally exercise the self-referential case
// instead of the multi-merge one it's named for.
func TestParseXLSX_OnlyOneOfSeveralMergesOverlappingStillRejects(t *testing.T) {
	f := excelize.NewFile()
	defer f.Close()
	for cell, v := range map[string]string{
		"A1": "Name", "B1": "Price", "C1": "Category", "E1": "Acme Wholesale Ltd",
		"A2": "Widget", "B2": "2.00", "C2": "Drinks",
	} {
		if err := f.SetCellStr("Sheet1", cell, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.MergeCell("Sheet1", "E1", "F1"); err != nil { // harmless decorative merge, outside every recognised column
		t.Fatal(err)
	}
	if err := f.MergeCell("Sheet1", "B2", "C2"); err != nil { // Price (top-left, survives) + Category (blanked) on the one data row
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := f.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	_, err := ParseXLSX(bytes.NewReader(buf.Bytes()), int64(buf.Len()), 2, testEnabledIDs, false)
	if !errors.Is(err, ErrXLSXMergedCells) {
		t.Errorf("err = %v, want ErrXLSXMergedCells (a harmless first merge must not mask a real one)", err)
	}
}

// TestParseXLSX_PartiallyOverlappingColumnMergeRejects: a merge that
// starts inside the recognised column span and ends outside it still
// overlaps the rectangle and must reject — the column check is a range
// overlap, not "fully contained" (ut-docs#1853 review finding 4: the
// span is deliberately not a precise column set, so a merge into an
// unused neighbouring column is still treated as unsafe). The merge
// starts AT the recognised "price" column (its top-left, so the header
// text survives) and extends into an unused column to its right —
// starting the other way around (an unrecognised column merged into
// "name") would blank the name header's own text instead and fail
// header recognition entirely, a different case already covered by
// TestParseXLSX_MergedTitleRowAsHeaderStillReportsNoNameColumn.
func TestParseXLSX_PartiallyOverlappingColumnMergeRejects(t *testing.T) {
	f := excelize.NewFile()
	defer f.Close()
	for cell, v := range map[string]string{
		"B1": "Name", "C1": "Price", // recognised columns are B (2) and C (3); D is unused
		"B2": "Widget", "C2": "2.00",
	} {
		if err := f.SetCellStr("Sheet1", cell, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.MergeCell("Sheet1", "C1", "D1"); err != nil { // C ("price", top-left) straddles into unused D
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := f.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	_, err := ParseXLSX(bytes.NewReader(buf.Bytes()), int64(buf.Len()), 2, testEnabledIDs, false)
	if !errors.Is(err, ErrXLSXMergedCells) {
		t.Errorf("err = %v, want ErrXLSXMergedCells (merge straddles from the recognised \"price\" column into an unused one, not fully outside the span)", err)
	}
}

// TestParseXLSX_MergedTitleRowAsHeaderStillReportsNoNameColumn: a merged
// title banner standing in for the header row behaves exactly like the
// unmerged case in TestParseXLSX_LeadingTitleRow — ErrNoNameColumn,
// never ErrXLSXMergedCells. Pins the precedence choice: header
// recognition is checked before the merge rectangle even has an idx to
// be built from, and that's the *more* consistent outcome (a title row
// standing in for the header fails the same way whether or not the
// title happens to be merged), not a regression — see rejectMergedCells'
// own doc comment.
func TestParseXLSX_MergedTitleRowAsHeaderStillReportsNoNameColumn(t *testing.T) {
	f := excelize.NewFile()
	defer f.Close()
	for cell, v := range map[string]string{
		"A1": "Catalog Export - Cafe Example - 2026-09-08",
		"A2": "Name", "B2": "Price",
		"A3": "Widget", "B3": "2.00",
	} {
		if err := f.SetCellStr("Sheet1", cell, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.MergeCell("Sheet1", "A1", "F1"); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := f.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	_, err := ParseXLSX(bytes.NewReader(buf.Bytes()), int64(buf.Len()), 2, testEnabledIDs, false)
	if !errors.Is(err, ErrNoNameColumn) {
		t.Errorf("err = %v, want ErrNoNameColumn (a merged title-row-as-header must fail the same way an unmerged one does)", err)
	}
}

// TestMergeOverlapsRectangle: a small table test pinning the pure
// row/column overlap arithmetic directly, independent of ParseXLSX's
// higher-level behaviour — the boundaries here are exactly what an
// off-by-one would silently break.
func TestMergeOverlapsRectangle(t *testing.T) {
	tests := []struct {
		name           string
		start, end     string
		maxRow         int
		minCol, maxCol int
		wantOverlap    bool
	}{
		{"inside both ranges", "A1", "B1", 3, 1, 2, true},
		{"row exactly at maxRow", "A3", "A3", 3, 1, 2, true},
		{"row one past maxRow", "A4", "A4", 3, 1, 2, false},
		{"column exactly at maxCol", "B1", "B1", 3, 1, 2, true},
		{"column one past maxCol", "C1", "C1", 3, 1, 2, false},
		{"straddles left column edge", "A1", "B1", 3, 2, 3, true},
		{"straddles right column edge", "C1", "D1", 3, 2, 3, true},
		{"reversed range (end before start)", "B3", "A1", 3, 1, 2, true},
		{"fully outside both", "D5", "E6", 3, 1, 2, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := excelize.MergeCell{tt.start + ":" + tt.end, ""}
			got, err := mergeOverlapsRectangle(m, tt.maxRow, tt.minCol, tt.maxCol)
			if err != nil {
				t.Fatalf("mergeOverlapsRectangle: %v", err)
			}
			if got != tt.wantOverlap {
				t.Errorf("mergeOverlapsRectangle(%s:%s, maxRow=%d, cols=[%d,%d]) = %v, want %v", tt.start, tt.end, tt.maxRow, tt.minCol, tt.maxCol, got, tt.wantOverlap)
			}
		})
	}
}

// TestDataRectangleLastRow: a small table test pinning
// dataRectangleLastRow directly — the header-only default, a gap
// resuming with more clean rows, and a trailing row that fails each of
// the two "clean item" conditions in turn.
func TestDataRectangleLastRow(t *testing.T) {
	idx := map[string]int{"name": 0, "price": 1}
	tests := []struct {
		name string
		rows [][]string
		want int
	}{
		{"header only, no data rows at all", [][]string{{"Name", "Price"}}, 1},
		{"single clean row", [][]string{{"Name", "Price"}, {"Widget", "2.00"}}, 2},
		{
			"gap resumes with more clean rows",
			[][]string{{"Name", "Price"}, {"Widget", "2.00"}, nil, {"Gadget", "3.00"}},
			4,
		},
		{
			"trailing row has no name — doesn't extend the bound",
			[][]string{{"Name", "Price"}, {"Widget", "2.00"}, nil, {"", "127.50"}},
			2,
		},
		{
			"trailing row has a name but unparseable price — doesn't extend the bound",
			[][]string{{"Name", "Price"}, {"Widget", "2.00"}, nil, {"Note: reconciled", ""}},
			2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := dataRectangleLastRow(tt.rows, idx, 2); got != tt.want {
				t.Errorf("dataRectangleLastRow(%v) = %d, want %d", tt.rows, got, tt.want)
			}
		})
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
