// Package catimport parses catalog exports from other POS systems
// (docs: architecture/catalog-import.md, G22a). Formats are detected from
// the CSV header row; per-format synonym tables map columns onto one
// neutral ImportItem. Parsing never touches the database — the pages layer
// previews, dedupes and commits.
package catimport

import (
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
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
	Issue       string  // non-empty = row cannot be imported (reason)
	// BarcodeIssue is non-empty when the CSV carried a non-empty barcode
	// value that normalizeBarcode discarded (unsupported shape — e.g. a
	// 4-digit produce PLU or an alphanumeric internal code). Unlike Issue,
	// this never blocks the row: the item still imports, but the caller
	// (pages) should surface this as a per-row warning so the operator
	// knows the barcode was silently dropped, not just missing (ut-docs#293,
	// same defect class as the AddBarcode fix — the symbology itself is
	// ut-docs#295's job, not this field's).
	BarcodeIssue string
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
		return Result{}, fmt.Errorf("no name column recognised — is this a catalog export?")
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
		rawBarcode := get(rec, "barcode")
		item := ImportItem{
			Name:        get(rec, "name"),
			SKU:         get(rec, "sku"),
			Barcode:     normalizeBarcode(rawBarcode),
			Category:    get(rec, "category"),
			Department:  get(rec, "department"),
			Description: get(rec, "description"),
			IsWeighed:   isTruthy(get(rec, "weighed")),
		}
		if rawBarcode != "" && item.Barcode == "" {
			item.BarcodeIssue = barcodeIssue(rawBarcode)
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
		switch {
		case item.Name == "":
			item.Issue = "missing name"
		case perr != nil:
			item.Issue = "bad price: " + get(rec, "price")
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

// barcodeIssue explains why normalizeBarcode discarded a non-empty raw
// barcode value, mirroring its checks exactly (never changes what shapes
// are accepted — that's ut-docs#295's call). Called only when the raw value
// was non-empty and normalizeBarcode returned "".
func barcodeIssue(raw string) string {
	s := strings.TrimSuffix(raw, ".0")
	for _, r := range s {
		if r < '0' || r > '9' {
			return fmt.Sprintf("barcode %q not imported: unsupported format (not a plain digit string)", raw)
		}
	}
	if len(s) < 6 {
		return fmt.Sprintf("barcode %q not imported: too short (needs 6-14 digits)", raw)
	}
	return fmt.Sprintf("barcode %q not imported: too long (needs 6-14 digits)", raw)
}

func isTruthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "y", "yes", "true", "1":
		return true
	}
	return false
}
