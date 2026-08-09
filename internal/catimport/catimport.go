// Package catimport parses catalog exports from other POS systems
// (docs: architecture/catalog-import.md, G22a). Formats are detected from
// the CSV header row; per-format synonym tables map columns onto one
// neutral ImportItem. Parsing never touches THIS application's own catalog
// database — the pages layer previews, dedupes and commits. (ParseBkp,
// ut-docs#511, is a partial exception in letter only: it opens a SQLite
// connection, but only against a temp file extracted from the operator's
// own uploaded .bkp backup — a foreign, read-only file, never the live
// catalog DB. Its actual SQL query text lives in internal/data, not here,
// so this package still carries no literal SQL of its own — see bkp.go and
// internal/data/bkp_products_repo.go's doc comments for why.)
package catimport

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

// ErrNoNameColumn is Parse's own reason code (ut-docs#303) for the single
// most common whole-file failure — a wrong-format upload — so the pages
// layer can show a translated message instead of this package's English
// text. Other Parse failures (a malformed CSV row, an unparseable header
// read) are rarer, lower-level io/csv-shaped errors not worth the same
// treatment yet; the caller still must not put THEIR raw text on screen
// either, see import_page.go's generic fallback + log.
var ErrNoNameColumn = errors.New("no name column recognised — is this a catalog export?")

// Issue/BarcodeIssue reason codes (ut-docs#303). catimport has no locale to
// translate into — it's a pure parser (package doc above) — so it reports a
// stable, machine-readable code plus whatever dynamic value the code needs,
// and the pages layer (which DOES have a locale) composes the operator-
// facing, translated message from them.
const (
	IssueMissingName = "missing_name"
	IssueBadPrice    = "bad_price"

	// The three below are ParseBkp's own reason codes (ut-docs#511, see
	// bkp.go) — kept here rather than duplicated there so
	// translateImportIssue's switch (import_page.go) and any other caller
	// has exactly one place to look for the full Issue* set, CSV or .bkp.
	IssueSourceDeleted      = "source_deleted"        // Status = 3 in the source till
	IssueNotSellable        = "not_sellable"          // an order-type toggle (e.g. "⚠️ToGo⚠️"), ProductType = 4
	IssueDuplicateSKUInFile = "duplicate_sku_in_file" // ProductNumber repeats within THIS file — distinct from import.status.sku_already_in_catalog, which is a DB-level check

	BarcodeIssueUnsupportedFormat = "unsupported_format"
	BarcodeIssueTooShort          = "too_short"
	BarcodeIssueTooLong           = "too_long"

	// TaxIssueUnparseable: a tax/takeaway-tax cell was present but didn't
	// read as a percentage. Non-blocking, like BarcodeIssue — but unlike
	// stock (silently optional), a dropped tax rate is compliance-sensitive
	// (ut-docs#512), so the drop is reported rather than swallowed.
	TaxIssueUnparseable = "unparseable"
)

// ImportItem is one parsed catalog row, prices in minor units.
type ImportItem struct {
	Name        string
	SKU         string
	Barcode     string
	PriceMinor  int64
	Category    string
	Department  string // enterprise/ERP masters carry a department axis
	Description string
	IsWeighed   bool
	Stock       float64 // opening quantity from the source system
	HasStock    bool    // the file carried a parseable stock value
	Issue       string  // one of the Issue* consts, non-empty = row cannot be imported
	IssueDetail string  // the row's dynamic value for reason codes that need one (e.g. the raw price string for IssueBadPrice)
	// BarcodeIssue is non-empty when the CSV carried a non-empty barcode
	// value that normalizeBarcode discarded (unsupported shape — e.g. a
	// 4-digit produce PLU or an alphanumeric internal code). Unlike Issue,
	// this never blocks the row: the item still imports, but the caller
	// (pages) should surface this as a per-row warning so the operator
	// knows the barcode was silently dropped, not just missing (ut-docs#293,
	// same defect class as the AddBarcode fix — the symbology itself is
	// ut-docs#295's job, not this field's). One of the BarcodeIssue* consts;
	// BarcodeIssueRaw is the raw value the reason applies to.
	BarcodeIssue    string
	BarcodeIssueRaw string
	// Tax rates, in basis points (1900 = 19%), from optional tax columns —
	// same optional-column pattern as Stock/HasStock: absent or blank ⇒
	// both false/zero, never blocks the row (ut-docs#512). TakeawayRateBP
	// is the source's distinct takeaway/off-premises rate where it carries
	// one (Germany's §12 UStG dine-in/takeaway split). The parser carries
	// both rates raw; deciding whether a pair needs a takeaway override is
	// the pages layer's job.
	TaxRateBP      int
	HasTax         bool
	TakeawayRateBP int
	HasTakeaway    bool
	// TaxIssue/TakeawayTaxIssue mirror BarcodeIssue for the two tax
	// columns: set (with the raw cell value alongside) when a present,
	// non-blank cell didn't parse as a percentage. Never blocks the row —
	// it imports at the till's default rate — but the pages layer surfaces
	// the drop as a per-row warning (reason codes, not prose: ut-docs#303).
	TaxIssue            string
	TaxIssueRaw         string
	TakeawayTaxIssue    string
	TakeawayTaxIssueRaw string
}

// Result is a parsed file.
type Result struct {
	Format string // loyverse | square | generic | generic-erp
	Items  []ImportItem
}

// column synonym sets, matched case-insensitively against trimmed headers.
var columnSynonyms = map[string][]string{
	"name":     {"name", "item name", "product name", "title"},
	"sku":      {"sku", "reference", "item code"},
	"barcode":  {"barcode", "gtin", "upc", "ean", "barcodes"},
	"price":    {"price", "default price", "retail price", "sale price", "price [gbp]"},
	"category": {"category", "category name", "group", "subcategory", "sub-category", "class"},
	// Department is a distinct axis from category (an ERP master carries both;
	// a top-level "department" that categories nest under). Kept separate so a
	// file with only a category leaves department empty.
	"department":  {"department", "dept"},
	"description": {"description", "details"},
	"weighed":     {"sold by weight", "weighed", "sold by weight (y/n)", "weighed (y/n)"},
	"stock":       {"in stock", "stock", "quantity", "qty", "in_stock", "on_hand", "on hand", "current quantity", "stock quantity", "opening stock"},
	// Tax columns (ut-docs#512): optional, ignored when absent so existing
	// Loyverse/Square exports are unchanged. "takeaway tax" carries the
	// off-premises rate where the source distinguishes one (§12 UStG).
	"tax":          {"tax", "tax %", "tax rate", "vat", "vat %", "vat rate"},
	"takeaway_tax": {"takeaway tax", "takeaway tax %", "takeaway vat", "takeaway rate", "takeaway tax rate"},
	// square-specific extras used only for detection / variation naming
	"variation": {"variation name"},
	"token":     {"token", "handle"},
}

func headerIndex(headers []string) map[string]int {
	idx := map[string]int{}
	for i, h := range headers {
		key := strings.ToLower(strings.TrimSpace(h))
		for field, syns := range columnSynonyms {
			if _, taken := idx[field]; taken {
				continue
			}
			for _, s := range syns {
				if key == s || strings.HasPrefix(key, s+" [") { // "Current Quantity [Main]"
					idx[field] = i
					break
				}
			}
		}
	}
	return idx
}

// DetectFormat names the source system from its header row.
func DetectFormat(headers []string) string {
	joined := strings.ToLower(strings.Join(headers, "|"))
	switch {
	case strings.Contains(joined, "handle") && strings.Contains(joined, "sold by weight"):
		return "loyverse"
	case strings.Contains(joined, "token") && strings.Contains(joined, "variation name"):
		return "square"
	case hasColumn(headers, "department"):
		// A department axis marks an enterprise/ERP master export (Ansar &c.)
		// rather than a plain corner-shop catalog. Broad synonym coverage
		// (stock, department, qty) still flows through the shared parser.
		return "generic-erp"
	default:
		return "generic"
	}
}

// hasColumn reports whether the header row carries a column mapping to field.
func hasColumn(headers []string, field string) bool {
	for _, h := range headers {
		key := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, "\uFEFF")))
		for _, s := range columnSynonyms[field] {
			if key == s || strings.HasPrefix(key, s+" [") {
				return true
			}
		}
	}
	return false
}

// csvFormulaTriggers mirrors internal/pages/csv_export.go's csvSafe —
// duplicated here (not imported: pages already imports catimport, so the
// reverse would cycle) because stripCSVDefuse needs the exact same set to
// safely reverse it. If csvSafe's trigger set ever changes, this one must
// change with it.
const csvFormulaTriggers = "=+-@\t\r"

// stripCSVDefuse reverses pages.csvSafe's leading-apostrophe formula
// defusing (ut-docs#321) so catalog export → import round-trips losslessly
// through our own importer, a documented migration path (G22b) — unlike
// the invoice/audit exports csvSafe was first written for, which are never
// re-imported, so a permanent leading "'" there is harmless.
//
// This only strips a leading "'" when the byte immediately after it is
// itself one of csvSafe's trigger chars — the exact two-byte shape csvSafe
// emits (it only ever adds "'" right before a trigger char). A value that
// merely happens to start with a genuine apostrophe not followed by a
// trigger char (e.g. "'Twas the night") is left completely alone: there is
// no way csvSafe could have produced it, so it was never ours to strip.
//
// Known, accepted lossy edge case (2026-08-06 review): csvSafe is NOT
// injective for values that already start with "'" immediately followed by
// a trigger char — csvSafe("'=X") == "'=X" (untouched: an apostrophe isn't
// itself a trigger char), which is byte-identical to csvSafe("=X") == "'=X". This
// function can't tell those two apart, so a pre-existing "'=X"-shaped value
// (vanishingly rare in real product data) loses its leading apostrophe on
// one export→import cycle, then stays stable ("=X" round-trips clean
// forever after). The spreadsheet stays safe either way — every export
// re-defuses whatever is currently stored — so this is data drift on an
// edge case, not a reopened injection hole. See ut-docs#321's review record
// for the full analysis; a fully lossless fix would need csvSafe itself
// (shared with invoice/audit) to double a pre-existing leading "'" before a
// trigger char, tracked as its own follow-up card rather than done here.
func stripCSVDefuse(field string) string {
	if len(field) < 2 || field[0] != '\'' {
		return field
	}
	if strings.IndexByte(csvFormulaTriggers, field[1]) >= 0 {
		return field[1:]
	}
	return field
}

// Parse reads a CSV export into neutral items. currencyDecimals drives
// price parsing (e.g. 2 for GBP: "1.40" → 140; 0 for IRT: "12000" → 12000).
func Parse(r io.Reader, currencyDecimals int) (Result, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1 // exports are ragged in the wild
	headers, err := cr.Read()
	if err != nil {
		return Result{}, fmt.Errorf("read header: %w", err)
	}
	// Strip a UTF-8 BOM Excel loves to add.
	if len(headers) > 0 {
		headers[0] = strings.TrimPrefix(headers[0], "\uFEFF")
	}
	res := Result{Format: DetectFormat(headers)}
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

	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return res, fmt.Errorf("row %d: %w", len(res.Items)+2, err)
		}
		rawBarcode := stripCSVDefuse(get(rec, "barcode"))
		item := ImportItem{
			Name:        stripCSVDefuse(get(rec, "name")),
			SKU:         stripCSVDefuse(get(rec, "sku")),
			Barcode:     normalizeBarcode(rawBarcode),
			Category:    stripCSVDefuse(get(rec, "category")),
			Department:  get(rec, "department"),
			Description: stripCSVDefuse(get(rec, "description")),
			IsWeighed:   isTruthy(get(rec, "weighed")),
		}
		if rawBarcode != "" && item.Barcode == "" {
			item.BarcodeIssue = barcodeIssueReason(rawBarcode)
			item.BarcodeIssueRaw = rawBarcode
		}
		// Square: variation name qualifies the item name ("Coffee — Large").
		if v := get(rec, "variation"); v != "" && !strings.EqualFold(v, "regular") && res.Format == "square" {
			item.Name = strings.TrimSpace(item.Name + " " + v)
		}
		price, perr := ParsePrice(get(rec, "price"), currencyDecimals)
		item.PriceMinor = price
		// Opening stock is optional and never blocks a row: a blank or
		// unparseable value just means "no stock carried over".
		if raw := get(rec, "stock"); raw != "" {
			if qty, err := strconv.ParseFloat(strings.ReplaceAll(raw, ",", ""), 64); err == nil {
				item.Stock, item.HasStock = qty, true
			}
		}
		// Tax columns are optional too, but — unlike stock — a present cell
		// that doesn't parse is reported (TaxIssue), not dropped silently:
		// the row still imports at the till's default rate, and the pages
		// layer warns the operator (ut-docs#512, compliance-sensitive).
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
		case item.Name == "":
			item.Issue = IssueMissingName
		case perr != nil:
			item.Issue = IssueBadPrice
			item.IssueDetail = get(rec, "price")
		}
		res.Items = append(res.Items, item)
	}
	return res, nil
}

// ParsePrice converts a human price string ("£1,234.50", "1.40", "12000")
// into minor units using the shop currency's decimal places.
func ParsePrice(s string, decimals int) (int64, error) {
	clean := strings.Map(func(r rune) rune {
		if (r >= '0' && r <= '9') || r == '.' || r == '-' {
			return r
		}
		return -1 // strips symbols, spaces, thousands separators
	}, s)
	if clean == "" || clean == "-" {
		return 0, fmt.Errorf("empty price")
	}
	f, err := strconv.ParseFloat(clean, 64)
	if err != nil || f < 0 {
		return 0, fmt.Errorf("unparseable price %q", s)
	}
	return int64(math.Round(f * math.Pow10(decimals))), nil
}

// ParseTaxRateBP converts a human tax-rate string ("19", "19%", "19.5") into
// basis points (1900, 1900, 1950), rounding to the nearest basis point. Same
// shape and strictness as ParsePrice: strip the % sign and whitespace, parse
// the rest as a non-negative decimal, error on anything else.
func ParseTaxRateBP(s string) (int, error) {
	clean := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "%"))
	clean = strings.TrimSpace(clean)
	if clean == "" {
		return 0, fmt.Errorf("empty tax rate")
	}
	f, err := strconv.ParseFloat(clean, 64)
	if err != nil || f < 0 {
		return 0, fmt.Errorf("unparseable tax rate %q", s)
	}
	return int(math.Round(f * 100)), nil
}

func normalizeBarcode(s string) string {
	// Sheets export barcodes as floats ("5000000000011.0") or scientific
	// notation; keep plain digit strings only, drop the rest.
	s = strings.TrimSuffix(s, ".0")
	for _, r := range s {
		if r < '0' || r > '9' {
			return ""
		}
	}
	if len(s) < 6 || len(s) > 14 {
		return ""
	}
	return s
}

// barcodeIssueReason explains WHY normalizeBarcode discarded a non-empty
// raw barcode value, mirroring its checks exactly (never changes what
// shapes are accepted — that's ut-docs#295's call). Called only when the
// raw value was non-empty and normalizeBarcode returned "". Returns a
// reason code (see the BarcodeIssue* consts), not prose — ut-docs#303.
func barcodeIssueReason(raw string) string {
	s := strings.TrimSuffix(raw, ".0")
	for _, r := range s {
		if r < '0' || r > '9' {
			return BarcodeIssueUnsupportedFormat
		}
	}
	if len(s) < 6 {
		return BarcodeIssueTooShort
	}
	return BarcodeIssueTooLong
}

func isTruthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "y", "yes", "true", "1":
		return true
	}
	return false
}
