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

// xlsMagic is the OLE2/CFBF signature every legacy binary Office file
// (.xls, .doc, .ppt, ...) starts with — distinct from .xlsx's ZIP
// signature (import_page.go's zipMagic; .xlsx is a ZIP container, .xls is
// not). Sniffed by LooksLikeLegacyXLS.
var xlsMagic = []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}

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
	f, err := excelize.OpenReader(io.NewSectionReader(r, 0, size))
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
// German-locale price/tax cells (AC3) need no special handling here: a
// genuinely-numeric Excel cell always round-trips through GetRows as a
// plain dot-decimal string (Excel's own locale-specific *display*
// formatting, e.g. "1.234,56", is not part of the stored value and
// excelize does not reproduce it) — and a cell the source stored as
// German-formatted TEXT is read back as that literal string and goes
// through ParsePrice exactly like a CSV cell would, which already handles
// decimal-comma notation (ut-docs#586). A leading title row or trailing
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
	f, err := excelize.OpenReader(io.NewSectionReader(r, 0, size))
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

	// Same "first occurrence wins" convention as Parse (ut-docs#1224/#1222).
	seenForBarcode := map[string]bool{}
	for _, rec := range rows[1:] {
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
		price, perr := ParsePrice(get(rec, "price"), currencyDecimals)
		item.PriceMinor = price
		if raw := get(rec, "stock"); raw != "" {
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
