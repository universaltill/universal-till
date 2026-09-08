package catimport

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/xuri/excelize/v2"
)

// ErrXLSXLegacyUnsupported/ErrXLSXMergedCells are ParseXLSX's own
// whole-file reason codes (ut-docs#1837), same pattern as
// ErrNoNameColumn/ErrBkp*: a stable sentinel so the pages layer can show a
// specific, translated message instead of raw parser text on the
// operator's screen (ut-docs#303's rule).
var (
	// ErrXLSXLegacyUnsupported: AC6 — legacy binary .xls is explicitly not
	// supported (a different, much older container format entirely; a
	// real reader would be a second dependency for a format actively
	// being retired). Detected by LooksLikeLegacyXLS before either parser
	// even runs, so the operator gets "save as .xlsx or CSV" rather than
	// the generic invalid_file message.
	ErrXLSXLegacyUnsupported = errors.New("legacy .xls workbooks are not supported — save as .xlsx or CSV")
	// ErrXLSXMergedCells: AC4 — "reject or report, never guess". A merged
	// cell only carries its value in the merge's top-left cell; every
	// other cell in the range reads back empty from GetRows, which can
	// silently shift or blank out neighbouring columns depending on where
	// the merge sits. Rather than reason about which merges are safely
	// decorative (e.g. a title banner above the header) and which aren't,
	// any merge anywhere in the sheet rejects the whole file with a
	// specific message telling the operator to remove merges or export
	// CSV instead — conservative, but never silently wrong.
	ErrXLSXMergedCells = errors.New("this workbook uses merged cells, which cannot be read reliably")
)

// xlsxMaxUnzipSize bounds the TOTAL uncompressed size of every member of
// an uploaded .xlsx before excelize buffers any of it (review finding,
// ut-docs#1837 — the same decompression-bomb class ParseBkp already guards
// with bkpMaxMetaSize/bkpMaxDBSize, which this path had no equivalent of).
// excelize's own default is 16GB (UnzipSizeLimit, templates.go), and
// ReadZipReader buffers every non-worksheet member whole in RAM: a ~200KB
// upload declaring a 200MB member is accepted and allocated today,
// measured, and the default lets that scale to 16GB — an OOM kill of the
// whole POS process (checkout included) from one manager-gated, or on a
// first-boot till anonymous, upload. 128MB is far above any real catalog
// workbook (a measured 8,000-row export with six columns is ~250KB on the
// wire and a couple of MB unzipped) and far below anything that can hurt
// the Pi-class hardware this runs on. Worksheet/sharedStrings members above
// excelize's derived UnzipXMLSizeLimit (min(this, 16MB)) still stream via a
// temp file that f.Close() removes, so RAM stays bounded well under this
// even at the cap.
const xlsxMaxUnzipSize = 128 << 20

// xlsxOpenOptions is the single place both excelize.OpenReader calls on
// this path (the sniff and the real parse) get their limits from, so the
// two can never drift apart.
func xlsxOpenOptions() excelize.Options {
	return excelize.Options{UnzipSizeLimit: xlsxMaxUnzipSize}
}

// xlsMagic is the OLE2/CFBF signature every legacy binary Office file
// (.xls, .doc, .ppt, ...) starts with — distinct from .xlsx's ZIP
// signature (import_page.go's zipMagic; .xlsx is a ZIP container, .xls is
// not). Sniffed by LooksLikeLegacyXLS.
var xlsMagic = []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}

// isBlankRow reports whether a worksheet row carries no non-whitespace
// value in any cell — GetRows's representation of a blank separator row,
// which encoding/csv drops silently on the CSV path (see ParseXLSX's loop).
func isBlankRow(rec []string) bool {
	for _, c := range rec {
		if strings.TrimSpace(c) != "" {
			return false
		}
	}
	return true
}

// LooksLikeLegacyXLS reports whether the first bytes of an upload are a
// legacy binary .xls (or other OLE2-container Office file). The pages
// layer sniffs this alongside the existing ZIP sniff (sniffZipUpload)
// before choosing a parser, so a legacy .xls upload gets
// ErrXLSXLegacyUnsupported's specific message (ut-docs#1837 AC6) rather
// than falling through to the generic import.error.invalid_file.
func LooksLikeLegacyXLS(header []byte) bool {
	return bytes.HasPrefix(header, xlsMagic)
}

// LooksLikeXLSXZip reports whether a ZIP-magic upload is actually an
// Office Open XML workbook rather than a speedy kasse / pepperm cashbox
// .bkp backup — both are plain ZIP containers with the identical
// PK-magic sniffZipUpload already checks, so the pages layer needs a
// second, content-based check to route correctly. .bkp requires
// "backup.db" + "meta.inf" (ParseBkp's own requirement); an .xlsx opens
// cleanly as a workbook with at least one sheet — checked directly here
// (rather than re-deriving it from ParseXLSX's own excelize.OpenReader
// call) so a corrupt/unrecognised ZIP that is neither still falls through
// to ParseBkp's existing ErrBkpMissingFiles → "bkp_unrecognised" message
// rather than a new, third failure path.
func LooksLikeXLSXZip(r io.ReaderAt, size int64) bool {
	f, err := excelize.OpenReader(io.NewSectionReader(r, 0, size), xlsxOpenOptions())
	if err != nil {
		return false
	}
	defer f.Close()
	return len(f.GetSheetList()) > 0
}

// ParseXLSX reads an Excel workbook export into neutral items, reusing the
// exact same column-detection and field-parsing helpers Parse (CSV) uses —
// DetectFormat/headerIndex for Loyverse/Square/SumUp/generic recognition,
// ParsePrice/ParseTaxRateBP/normalizeBarcode for cell values — so a
// spreadsheet and a CSV of the same export behave identically (ut-docs#1837
// AC1). Deliberately a separate loop from Parse rather than a shared-record
// refactor of it: Parse's existing streaming CSV loop (and its tests) stay
// completely untouched, at the cost of duplicating the per-row field
// mapping once here.
//
// Only the first worksheet is read (AC2) — GetRows already returns each
// row trimmed to its own extent (ragged, like CSV's FieldsPerRecord=-1).
// A row with no non-empty cell at all is skipped outright, matching
// encoding/csv's own handling of a blank line so a blank separator row in
// a spreadsheet doesn't become a phantom "missing name and bad price"
// problem row the CSV of the same export would never produce (AC1).
//
// Cell values (AC3) come from GetRows, which APPLIES each cell's number
// format — that is what turns a percent-stored tax cell (0.19 under a "0%"
// format) into the "19%" ParseTaxRateBP expects, and it renders grouped or
// currency-suffixed money ("1,234.56", "1.234,56 €") into forms ParsePrice
// already normalises (ut-docs#586). It is NOT safe for money on its own,
// though: a display format that ROUNDS (e.g. "#,##0" or "0" on a stored
// 1.4) renders as "1", and importing that would silently price a €1.40
// item at €1.00 — so the price and stock columns are read from a second,
// RAW pass (getNum below) and only fall back to the formatted text when
// the raw cell isn't a plain number (a text-stored German price such as
// "1.234,56" comes back byte-identical from either pass and goes through
// ParsePrice exactly like a CSV cell would). Tax deliberately keeps the
// formatted value: its raw counterpart for a percent-formatted cell is
// 0.19, which parses "successfully" as 0.19% — silently wrong on a
// compliance-sensitive field. A leading title row or trailing
// totals row (AC4) is not specially detected: a title row fails the same
// "no name column recognised" check a malformed CSV header would
// (ErrNoNameColumn), and a totals row surfaces the same per-row
// IssueMissingName/IssueBadPrice warning any malformed CSV row would —
// reported on the preview grid, never silently imported. Merged cells
// (AC4) DO get an explicit whole-file rejection (ErrXLSXMergedCells) — see
// its own doc comment for why guessing isn't attempted there.
//
// Deliberately does NOT run cell values through stripCSVDefuse: that
// function reverses THIS app's own CSV-export formula-defusing (a leading
// "'" this app itself added, see csvSafe) so re-importing your own
// export doesn't show a literal "'=FOO" — a CSV-round-trip concern that
// doesn't apply to a third-party spreadsheet export.
func ParseXLSX(r io.ReaderAt, size int64, currencyDecimals int, enabledSymbologyIDs []string, useItemNumbersAsBarcodes bool) (Result, error) {
	f, err := excelize.OpenReader(io.NewSectionReader(r, 0, size), xlsxOpenOptions())
	if err != nil {
		return Result{}, fmt.Errorf("open xlsx: %w", err)
	}
	defer f.Close()

	sheets := f.GetSheetList()
	if len(sheets) == 0 {
		return Result{}, fmt.Errorf("open xlsx: no worksheets")
	}
	sheetName := sheets[0]

	merged, merr := f.GetMergeCells(sheetName)
	if merr != nil {
		return Result{}, fmt.Errorf("read merged cells: %w", merr)
	}
	if len(merged) > 0 {
		return Result{}, ErrXLSXMergedCells
	}

	rows, rerr := f.GetRows(sheetName)
	if rerr != nil {
		return Result{}, fmt.Errorf("read rows: %w", rerr)
	}
	if len(rows) == 0 {
		return Result{}, fmt.Errorf("read header: %w", io.EOF)
	}

	headers := rows[0]
	if len(headers) > 0 {
		headers[0] = strings.TrimPrefix(headers[0], "\uFEFF") // parity with Parse's BOM strip
	}
	res := Result{Format: DetectFormat(headers), SheetName: sheetName}
	idx := headerIndex(headers)
	if _, ok := idx["name"]; !ok {
		return Result{}, ErrNoNameColumn
	}

	get := func(rec []string, field string) string {
		i, ok := idx[field]
		if !ok || i >= len(rec) {
			return ""
		}
		return strings.TrimSpace(rec[i])
	}

	// Second, RAW pass for the numeric columns only — see the doc comment
	// above for why the formatted pass alone silently rounds money. Skipped
	// entirely when the file carries neither column, so a workbook that
	// needs it doesn't pay for one that doesn't.
	var rawRows [][]string
	_, hasPrice := idx["price"]
	_, hasStock := idx["stock"]
	if hasPrice || hasStock {
		rawRows, rerr = f.GetRows(sheetName, excelize.Options{RawCellValue: true})
		if rerr != nil {
			return Result{}, fmt.Errorf("read raw rows: %w", rerr)
		}
	}
	// getNum returns the value to parse for a numeric column: the raw,
	// unformatted cell when it is a plain number (no display rounding, no
	// grouping, no currency suffix), else the formatted text — which is
	// what a text-stored cell ("1.234,56") and an empty cell both land on.
	getNum := func(rowNo int, rec []string, field string) string {
		i, ok := idx[field]
		if ok && rowNo < len(rawRows) && i < len(rawRows[rowNo]) {
			if raw := strings.TrimSpace(rawRows[rowNo][i]); raw != "" {
				if _, err := strconv.ParseFloat(raw, 64); err == nil {
					return raw
				}
			}
		}
		return get(rec, field)
	}

	// Same "first occurrence wins" convention as Parse (ut-docs#1224/#1222).
	seenForBarcode := map[string]bool{}
	for rowNo, rec := range rows[1:] {
		rowNo++ // rows[0] is the header; rowNo now indexes rows/rawRows alike
		// A wholly-empty row is a blank line, not a row with problems:
		// encoding/csv skips those outright, so Parse never sees one, and a
		// spreadsheet's own blank separator row must behave the same (AC1)
		// instead of surfacing a phantom missing-name/bad-price row on the
		// preview grid for the operator to puzzle over.
		if isBlankRow(rec) {
			continue
		}
		rawBarcode := get(rec, "barcode")
		dec, barcodeMatched := normalizeBarcode(rawBarcode, enabledSymbologyIDs)
		item := ImportItem{
			Name:        get(rec, "name"),
			SKU:         get(rec, "sku"),
			Barcode:     dec.LookupKey,
			BarcodeType: dec.SymbologyID,
			Category:    get(rec, "category"),
			Department:  get(rec, "department"),
			Description: get(rec, "description"),
			IsWeighed:   isTruthy(get(rec, "weighed")),
		}
		if rawBarcode != "" && !barcodeMatched {
			item.BarcodeIssue = BarcodeIssueNoSymbologyMatch
			item.BarcodeIssueRaw = rawBarcode
		}
		if v := get(rec, "variation"); v != "" && !strings.EqualFold(v, "regular") && res.Format == "square" {
			item.Name = strings.TrimSpace(item.Name + " " + v)
		}
		price, perr := ParsePrice(getNum(rowNo, rec, "price"), currencyDecimals)
		item.PriceMinor = price
		if raw := getNum(rowNo, rec, "stock"); raw != "" {
			if qty, err := strconv.ParseFloat(strings.ReplaceAll(raw, ",", ""), 64); err == nil {
				item.Stock, item.HasStock = qty, true
			}
		}
		if raw := get(rec, "tax"); raw != "" {
			if bp, err := ParseTaxRateBP(raw); err == nil {
				item.TaxRateBP, item.HasTax = bp, true
			} else {
				item.TaxIssue, item.TaxIssueRaw = TaxIssueUnparseable, raw
			}
		}
		if raw := get(rec, "takeaway_tax"); raw != "" {
			if bp, err := ParseTaxRateBP(raw); err == nil {
				item.TakeawayRateBP, item.HasTakeaway = bp, true
			} else {
				item.TakeawayTaxIssue, item.TakeawayTaxIssueRaw = TaxIssueUnparseable, raw
			}
		}
		switch {
		case item.Name == "" && perr != nil:
			item.Issue = IssueMissingNameAndBadPrice
			item.IssueDetail = get(rec, "price")
		case item.Name == "":
			item.Issue = IssueMissingName
		case perr != nil:
			item.Issue = IssueBadPrice
			item.IssueDetail = get(rec, "price")
		}
		if useItemNumbersAsBarcodes && item.Barcode == "" && item.Issue == "" && item.SKU != "" {
			if seenForBarcode[item.SKU] {
				item.BarcodeIssue = BarcodeIssueDuplicateItemNumber
				item.BarcodeIssueRaw = item.SKU
			} else {
				seenForBarcode[item.SKU] = true
				item.Barcode, item.BarcodeType, item.BarcodeIssue, item.BarcodeIssueRaw = DeriveNumberBarcode(item.SKU, enabledSymbologyIDs)
			}
		}
		res.Items = append(res.Items, item)
	}
	return res, nil
}
