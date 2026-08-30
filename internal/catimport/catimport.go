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

	"github.com/universaltill/universal-till/internal/barcode"
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

	// BarcodeIssueNoSymbologyMatch: the CSV carried a non-empty barcode
	// value that matched none of the shop's enabled internal/barcode
	// registry entries (ADR-0059 Decision §3) — the same named-rejection
	// shape as CatalogRepo.AddBarcode's ErrBarcodeNoSymbologyMatch, just a
	// reason code here instead of an error (catimport has no locale, see
	// above). Only reachable once a shop has disabled the default
	// permissive catch-alls (CODE128/INTERNAL_PLU) — under the default
	// enabled set, Match never rejects a non-empty code (ADR-0059 §2).
	BarcodeIssueNoSymbologyMatch = "no_symbology_match"

	// BarcodeIssueDuplicateItemNumber: useItemNumbersAsBarcodes (ut-docs#1224)
	// is on, but this row's item number/SKU is claimed by an earlier row in
	// the same file — deriving a barcode from it would make two distinct
	// items scan to the same code, so this row is left without one. The
	// item itself still imports (its SKU is untouched by this — see
	// SKUIssueDuplicateInFile for that, a separate, .bkp-only concern);
	// only the barcode derivation is skipped.
	BarcodeIssueDuplicateItemNumber = "duplicate_item_number"

	// TaxIssueUnparseable: a tax/takeaway-tax cell was present but didn't
	// read as a percentage. Non-blocking, like BarcodeIssue — but unlike
	// stock (silently optional), a dropped tax rate is compliance-sensitive
	// (ut-docs#512), so the drop is reported rather than swallowed.
	TaxIssueUnparseable = "unparseable"

	// SKUIssueDuplicateInFile: an otherwise-clean row's source product
	// number was already claimed by an earlier row in this same file
	// (ut-docs#1222) — the source reused one PLU across several distinct
	// products (verified against a real pilot backup: one PLU shared by
	// six different products). Non-blocking, like BarcodeIssue/TaxIssue:
	// the row still imports, under a synthesized SKU (the original PLU
	// plus a "-N" suffix), rather than being silently dropped the way
	// IssueDuplicateSKUInFile used to drop it.
	SKUIssueDuplicateInFile = "duplicate_in_file"

	// ImageIssueUnresolved/ImageIssueTooLarge (ut-docs#1223): ParseBkp's own
	// reason codes for a row whose source carried a ProductImagePath that
	// didn't turn into usable image bytes — either the .bkp archive has no
	// documents.zip member matching the path (no such archive at all, or a
	// dangling reference), or a matching member exists but is past the size
	// this importer will read into memory. Non-blocking, same pattern as
	// BarcodeIssue/TaxIssue/SKUIssue: the row still imports, just without a
	// real photo — it falls back to the existing placeholder-icon path
	// (ut-docs#1189) — and the pages layer surfaces this as a per-row
	// warning rather than silently dropping the reference.
	ImageIssueUnresolved = "unresolved"
	ImageIssueTooLarge   = "too_large"
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
	// BarcodeType is the internal/barcode registry id that Barcode matched
	// (ADR-0059, ut-docs#936), e.g. "EAN13" / "CODE128" / "INTERNAL_PLU".
	// Empty when Barcode is empty. The pages layer passes this to
	// CatalogRepo.AddBarcode as an explicit BarcodeType so AddBarcode's
	// inference path does NOT re-run the registry match on a Barcode this
	// package has already decoded — Barcode already holds the decoded
	// LookupKey, and re-matching that key (rather than the raw code) can
	// classify it as a different, wrong symbology (e.g. an embedded-data
	// LookupKey re-decodes to CODE128, or — under a narrowed enabled set —
	// fails the second match entirely). Decode once, here.
	BarcodeType string
	// BarcodeIssue is non-empty when the CSV carried a non-empty barcode
	// value that matched none of the shop's enabled symbologies (ADR-0059
	// Decision §3, ut-docs#936) — reachable only once a shop narrows its
	// enabled set away from the default permissive catch-alls
	// (CODE128/INTERNAL_PLU). Unlike Issue, this never blocks the row: the
	// item still imports, but the caller (pages) should surface this as a
	// per-row warning so the operator knows the barcode was dropped, not
	// just missing (ut-docs#293, same defect class as the AddBarcode fix).
	// One of the BarcodeIssue* consts; BarcodeIssueRaw is the raw value the
	// reason applies to.
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
	// SKUIssue/SKUIssueRaw mirror BarcodeIssue/TaxIssue for the source
	// product number (ut-docs#1222): set (with the original PLU alongside)
	// when a reused source number was de-duplicated with a suffix rather
	// than dropped. Never blocks the row — it imports under the suffixed
	// SKU — but the pages layer surfaces it as a per-row warning so the
	// operator knows the number was reused, same reasoning as the other
	// two non-blocking Issue pairs above.
	SKUIssue    string
	SKUIssueRaw string
	// ImageData is the resolved image bytes for this row (ut-docs#1223) —
	// set only by ParseBkp, when the source's ProductImagePath resolved to
	// a member of the .bkp archive's documents.zip. Empty for the CSV path
	// (no source column for it yet) and for any .bkp row with no image
	// reference at all. The pages layer decodes/re-encodes and writes this
	// to disk at commit time, the same way a manual photo upload does —
	// this package stays a pure, disk-free parser (package doc above).
	ImageData []byte
	// ImageIssue/ImageIssueRaw mirror BarcodeIssue/TaxIssue/SKUIssue: set
	// when the row carried a non-empty ProductImagePath that did not
	// resolve to usable image bytes. One of the ImageIssue* consts;
	// ImageIssueRaw is the source's own path string. Never blocks the row.
	ImageIssue    string
	ImageIssueRaw string
}

// Result is a parsed file.
type Result struct {
	Format string // loyverse | square | sumup | generic | generic-erp
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
	// off-premises rate where the source distinguishes one (§12 UStG). A
	// trailing parenthesised suffix on any field (e.g. SumUp's own "Tax
	// rate (%)", ut-docs#581) is handled generally by stripTrailingParen
	// below, not by a growing list of literal entries here (ut-docs#587).
	"tax":          {"tax", "tax %", "tax rate", "vat", "vat %", "vat rate"},
	"takeaway_tax": {"takeaway tax", "takeaway tax %", "takeaway vat", "takeaway rate", "takeaway tax rate"},
	// square-specific extras used only for detection / variation naming
	"variation": {"variation name"},
	"token":     {"token", "handle"},
}

// stripTrailingParen removes one trailing parenthesised suffix (and the
// whitespace before it) from an already-lowercased header key, e.g.
// "tax rate(%)" -> "tax rate", "vat rate (%)" -> "vat rate", "tax (%)" ->
// "tax". Used as a second-pass match key alongside the raw key so one
// synonym list entry like "tax rate" catches a family of "(...)"-suffixed
// header variants (percent sign, currency code, ...) without a growing
// list of literal strings (ut-docs#587) — mirrors the existing " ["
// bracket-prefix rule's spirit: an additional lenient match, never a
// replacement for the exact-match pass.
func stripTrailingParen(key string) string {
	if !strings.HasSuffix(key, ")") {
		return key
	}
	open := strings.LastIndexByte(key, '(')
	if open < 0 {
		return key
	}
	return strings.TrimSpace(key[:open])
}

func headerIndex(headers []string) map[string]int {
	idx := map[string]int{}
	keys := make([]string, len(headers))
	stripped := make([]string, len(headers))
	for i, h := range headers {
		keys[i] = strings.ToLower(strings.TrimSpace(h))
		stripped[i] = stripTrailingParen(keys[i])
	}
	// Pass 1: exact match / " [" bracket-prefix rule only. A trailing
	// parenthetical often qualifies or negates the synonym it's attached
	// to ("Price (cost)", "VAT (takeaway)", "Stock (reserved)") rather
	// than being decorative, so the lenient paren-stripped match below
	// must never outrank a genuine exact match found here — review
	// finding B1, ut-docs#587: an earlier lenient match used to be able
	// to claim a field before a later, unrelated header's exact match
	// got a turn, silently importing e.g. a cost price as the retail
	// price or a takeaway tax rate as the dine-in rate.
	for i, key := range keys {
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
	// Pass 2: paren-stripped fallback (ut-docs#587), only for fields no
	// header claimed exactly above.
	for i, key := range stripped {
		if key == "" { // e.g. a header that's nothing but "(...)"
			continue
		}
		for field, syns := range columnSynonyms {
			if _, taken := idx[field]; taken {
				continue
			}
			for _, s := range syns {
				if key == s { // "Tax rate(%)" -> "tax rate"
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
	case strings.Contains(joined, "set up different prices and vat for takeaway"):
		// SumUp's items-export (ut-docs#581): this takeaway-VAT-toggle column
		// name is effectively unique to SumUp.
		return "sumup"
	case hasColumn(headers, "department"):
		// A department axis marks an enterprise/ERP master export (Ansar &c.)
		// rather than a plain corner-shop catalog. Broad synonym coverage
		// (stock, department, qty) still flows through the shared parser.
		// Checked before the sumup fallback below: an ERP master's own ID
		// columns (e.g. "Item Identifier") must never be mistaken for
		// SumUp's "Item id"/"Variant id" (ut-docs#581 review finding M1).
		//
		// hasColumn strips a trailing parenthetical (ut-docs#587), so a
		// decorated header like "Dept (code)" also lands here. That is the
		// deliberately-chosen behaviour (ut-docs#705): a department axis is a
		// department axis however its header is qualified, so department wins
		// over the sumup fallback signature even when the header carries a
		// paren suffix — the same "department wins" rule as M1, extended to the
		// paren-lenient match. Pinned by
		// TestDetectFormatParenSuffixDepartmentWinsOverSumUpFallback.
		return "generic-erp"
	case hasExactHeader(headers, "item id") && hasExactHeader(headers, "variant id"):
		// Fallback SumUp signature for an export missing the takeaway-toggle
		// column above (that column is itself optional in a merchant's SumUp
		// account settings). Exact per-header match, not a substring Contains
		// on the joined string — "Item Identifier"/"Parent Item Id" must not
		// collide with this (ut-docs#581 review finding M1).
		return "sumup"
	default:
		return "generic"
	}
}

// hasExactHeader reports whether any header, trimmed/lowercased (and with a
// leading BOM stripped), equals name exactly — stricter than hasColumn's
// synonym+bracket-suffix matching, for detection signatures that must not
// substring-match a longer, unrelated header.
func hasExactHeader(headers []string, name string) bool {
	for _, h := range headers {
		if strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, "\uFEFF"))) == name {
			return true
		}
	}
	return false
}

// hasColumn reports whether the header row carries a column mapping to field.
func hasColumn(headers []string, field string) bool {
	for _, h := range headers {
		key := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, "\uFEFF")))
		stripped := stripTrailingParen(key)
		for _, s := range columnSynonyms[field] {
			if key == s || strings.HasPrefix(key, s+" [") || stripped == s {
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
// enabledSymbologyIDs is the shop's enabled internal/barcode registry ids
// (data.SettingsRepo.EnabledBarcodeSymbologies) — the caller reads this
// from the data layer (this package touches no DB, see the package doc)
// and passes it through so a CSV barcode is judged by the same shared
// registry matcher as AddBarcode and the scan path (ut-docs#936,
// ADR-0059 Decision §3's "call the same function, don't reimplement it").
// useItemNumbersAsBarcodes is the operator's per-import opt-in (ut-docs#1224,
// asked by the pages layer only for a catalog whose barcode column is empty
// or absent) — when true, a row with no barcode of its own gets one derived
// from its SKU/item number via deriveNumberBarcode, unless that number is
// shared by an earlier row in this file (BarcodeIssueDuplicateItemNumber).
func Parse(r io.Reader, currencyDecimals int, enabledSymbologyIDs []string, useItemNumbersAsBarcodes bool) (Result, error) {
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

	// Tracks which SKUs have already claimed a number-derived barcode this
	// file, for useItemNumbersAsBarcodes below — first occurrence of a given
	// SKU wins it, same "first claims it" convention ParseBkp's own in-file
	// dedup already uses for the SKU itself (ut-docs#1222).
	seenForBarcode := map[string]bool{}
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return res, fmt.Errorf("row %d: %w", len(res.Items)+2, err)
		}
		rawBarcode := stripCSVDefuse(get(rec, "barcode"))
		dec, barcodeMatched := normalizeBarcode(rawBarcode, enabledSymbologyIDs)
		item := ImportItem{
			Name:        stripCSVDefuse(get(rec, "name")),
			SKU:         stripCSVDefuse(get(rec, "sku")),
			Barcode:     dec.LookupKey,
			BarcodeType: dec.SymbologyID,
			Category:    stripCSVDefuse(get(rec, "category")),
			Department:  get(rec, "department"),
			Description: stripCSVDefuse(get(rec, "description")),
			IsWeighed:   isTruthy(get(rec, "weighed")),
		}
		if rawBarcode != "" && !barcodeMatched {
			item.BarcodeIssue = BarcodeIssueNoSymbologyMatch
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
		// useItemNumbersAsBarcodes (ut-docs#1224): only a row with no barcode
		// of its own, that cleanly imports, and that carries an item number
		// is eligible — and only its FIRST occurrence in the file claims the
		// derived barcode, so two distinct items sharing one number never
		// end up scannable as the same thing.
		if useItemNumbersAsBarcodes && item.Barcode == "" && item.Issue == "" && item.SKU != "" {
			if seenForBarcode[item.SKU] {
				item.BarcodeIssue = BarcodeIssueDuplicateItemNumber
				item.BarcodeIssueRaw = item.SKU
			} else {
				seenForBarcode[item.SKU] = true
				item.Barcode, item.BarcodeType, item.BarcodeIssue, item.BarcodeIssueRaw = deriveNumberBarcode(item.SKU, enabledSymbologyIDs)
			}
		}
		res.Items = append(res.Items, item)
	}
	return res, nil
}

// deriveNumberBarcode runs an item number (a PLU/SKU with no barcode of its
// own) through the same shared registry matcher normalizeBarcode/AddBarcode
// use (ADR-0059 Decision §3) — this is useItemNumbersAsBarcodes's (ut-docs#1224)
// only source of truth for what symbology such a number lands on: a plain
// numeric PLU falls through the checksum-validated entries (EAN13/EAN8/UPCA/
// UPCE/GTIN14 all reject a non-conforming length) to one of the permissive
// catch-alls (typically CODE128), stored verbatim, no check-digit meaning
// implied — never a real EAN. Returns an empty barcode/type and a non-empty
// issue when the shop's own narrowed enabled set rejects the number outright
// (only reachable once a shop has disabled the default catch-alls).
func deriveNumberBarcode(number string, enabledSymbologyIDs []string) (code, codeType, issue, issueRaw string) {
	dec, matched := normalizeBarcode(number, enabledSymbologyIDs)
	if !matched {
		return "", "", BarcodeIssueNoSymbologyMatch, number
	}
	return dec.LookupKey, dec.SymbologyID, "", ""
}

// normalizeDecimalComma rewrites a price string that uses a German/European
// decimal comma ("3,50", "1.234,50") into plain dot-decimal form, and leaves
// every other string alone (or strips repeated separators as thousands
// grouping — see below). It generalises the heuristic parseBkpSalesPrice
// (bkp.go, ut-docs#511) proved on the .bkp path, now shared by ParsePrice
// (ut-docs#586). decimals is the currency's minor-unit exponent (0 for
// JPY/IRR/IRT/IQD/AFN, 2 for most others) — needed because a lone separator
// followed by exactly 3 digits is genuinely ambiguous between "decimal
// point, 3-decimal currency" and "thousands separator, whole-number
// currency," and the two must not be resolved the same way:
//
//   - No separator at all → returned unchanged, so no-separator ("12000")
//     inputs are byte-identical to before.
//   - Both ',' and '.' present → whichever appears LAST is the decimal
//     point, the other is a thousands separator — always unambiguous
//     regardless of digit count. So "3,50", "1.234,50" mean decimal comma
//     (strip '.', turn ',' into '.'), while "£1,234.50" keeps the existing
//     comma-as-thousands behaviour.
//   - Only one separator KIND, but it repeats ("1,234,567", "1.234.567") →
//     that can only be thousands grouping, never a decimal point — stripped
//     entirely. (Review finding #2, ut-docs#586: the naive last-wins rule
//     turned a repeated comma into multi-dot garbage and hard-errored a
//     previously-valid English thousands-grouped price.)
//   - Exactly one separator, followed by exactly 3 digits and nothing else
//     (the accepted ambiguity: e.g. "2,900" could mean the thousands-
//     grouped €2,900.00, or the decimal-comma €2.90) → resolved by
//     decimals: a zero-decimal currency can never have a genuine
//     fractional part, so it's always the thousands-grouped reading there
//     ("12,000" with decimals=0 is 12000, not 12 — review finding #1,
//     ut-docs#586: the till ships five zero-decimal currencies, and the
//     naive rule silently under-parsed a plain thousands-grouped
//     IRR/IRT/IQD/AFN/JPY price by 1000x, the same class of silent
//     money-corruption defect this card exists to fix, just pointed at a
//     different set of shops). A non-zero-decimals currency keeps the
//     decimal-comma reading, consistent with the German sources
//     `parseBkpSalesPrice` was built for.
//   - Exactly one separator, followed by any other digit count (1-2, or
//     4+) → unambiguously a decimal point (comma) or unchanged (dot,
//     matching ParsePrice's original default).
//
// Known accepted residual edge case, inherited from the same tradeoff
// `parseBkpSalesPrice` already accepted: a malformed trailing separator
// with nothing after it (e.g. "3.50,") still resolves as a decimal point
// with an empty fraction rather than erroring — rare enough in real
// exports not to be worth its own branch.
func normalizeDecimalComma(s string, decimals int) string {
	commaCount := strings.Count(s, ",")
	dotCount := strings.Count(s, ".")
	lastComma := strings.LastIndexByte(s, ',')
	lastDot := strings.LastIndexByte(s, '.')

	switch {
	case commaCount == 0 && dotCount == 0:
		return s
	case commaCount > 0 && dotCount > 0:
		if lastComma > lastDot {
			s = strings.ReplaceAll(s, ".", "")
			return strings.ReplaceAll(s, ",", ".")
		}
		return strings.ReplaceAll(s, ",", "")
	case commaCount > 1 || dotCount > 1:
		// Repeated separator, one kind only: this can only be thousands
		// grouping IF the group after the last separator is a clean 3
		// digits — genuine grouping always looks like that ("1,234,567",
		// "1.234.567", Indian "1,23,456"). Anything else sharing the same
		// separator character is not grouping — a German date
		// ("12.05.2026", last group "2026", 4 digits) or column-mapping
		// garbage ("1.2.3", last group "3", 1 digit) — and must NOT be
		// silently squashed into a huge number (review finding N2,
		// ut-docs#586 round 2: stripping unconditionally turned
		// "12.05.2026" into a silent €12,052,026.00). Leave the
		// separators in place instead: strconv.ParseFloat can't parse a
		// multi-dot or any-comma string, so this reliably falls through
		// to ParsePrice's existing "unparseable price" error — the same
		// fail-loud outcome this shape already had before ut-docs#586.
		sep, last := byte(','), strings.LastIndexByte(s, ',')
		if dotCount > 1 {
			sep, last = '.', strings.LastIndexByte(s, '.')
		}
		if trailingDigits(s[last+1:]) == 3 {
			return strings.ReplaceAll(s, string(sep), "")
		}
		return s
	default:
		// Exactly one separator, of one kind. The digit run immediately
		// following it (before any trailing currency symbol/code, e.g.
		// "12,000 IRR" or "1,200 ﷼" — review finding N1, ut-docs#586
		// round 2: checking the whole untrimmed tail for "exactly 3
		// digits" failed the instant anything followed the digits, which
		// is exactly how this product's own zero-decimal currencies
		// render — FormatMoney puts IRR/IRT/IQD/AFN's symbol *after* the
		// grouped number) is what decides the ambiguous-3-digit case.
		sep, last := byte(','), lastComma
		if dotCount == 1 {
			sep, last = '.', lastDot
		}
		if trailingDigits(s[last+1:]) == 3 && decimals == 0 {
			return strings.ReplaceAll(s, string(sep), "")
		}
		if sep == ',' {
			return strings.ReplaceAll(s, ",", ".")
		}
		return s // sole '.', not the ambiguous case: unchanged (existing default)
	}
}

// trailingDigits returns the length of the run of ASCII digits at the start
// of s — used by normalizeDecimalComma to find how many digits immediately
// follow a separator, stopping at the first non-digit (a currency symbol,
// code, or trailing junk) rather than requiring the whole remainder of the
// string to be digits.
func trailingDigits(s string) int {
	n := 0
	for n < len(s) && s[n] >= '0' && s[n] <= '9' {
		n++
	}
	return n
}

// ParsePrice converts a human price string ("£1,234.50", "1.40", "12000")
// into minor units using the shop currency's decimal places. German
// decimal-comma notation ("3,50", "1.234,50") is handled too (ut-docs#586)
// via normalizeDecimalComma's separator-disambiguation heuristic — see its
// doc comment, including the accepted "2,900" ambiguity and how it's
// resolved differently for zero-decimal currencies.
func ParsePrice(s string, decimals int) (int64, error) {
	clean := strings.Map(func(r rune) rune {
		if (r >= '0' && r <= '9') || r == '.' || r == '-' {
			return r
		}
		return -1 // strips symbols, spaces, thousands separators
	}, normalizeDecimalComma(s, decimals))
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
	// Review finding B1 (ut-docs#512, 2026-08-09): strconv.ParseFloat
	// accepts "NaN"/"Inf"/"infinity" and neither is < 0, so they used to
	// sail past this guard and int(math.Round(NaN*100)) silently rounds to
	// math.MinInt64 — a persisted tax_codes.rate_basis_points of
	// -9223372036854775808 on the exact compliance-sensitive path this
	// card exists to protect, with no warning at all. IsNaN/IsInf must be
	// checked BEFORE the < 0 comparison — NaN < 0 and +Inf < 0 are both
	// false, so relying on that comparison alone lets them through. The
	// upper bound (a tax rate is never a real percentage above 100) also
	// catches a merchant typing basis points ("1900") or scientific
	// notation ("1e3") where a plain percentage was expected — today that
	// would otherwise silently create a 1900%/1000% tax code.
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 || f > 100 {
		return 0, fmt.Errorf("unparseable tax rate %q", s)
	}
	return int(math.Round(f * 100)), nil
}

// normalizeBarcode matches a raw CSV barcode cell against enabledIDs via
// the shared internal/barcode registry (ADR-0059 Decision §3) — the same
// Match function AddBarcode and the scan-path lookup call, so import and
// scan can never disagree about what a code means (ut-docs#936). Returns
// the barcode.Decoded (SymbologyID + LookupKey, plus any embedded data)
// and ok=true on a match; a zero Decoded and ok=false (raw non-empty, no
// enabled symbology matched) is what BarcodeIssueNoSymbologyMatch reports.
func normalizeBarcode(s string, enabledIDs []string) (barcode.Decoded, bool) {
	// Sheets export barcodes as floats ("5000000000011.0"); strip a
	// trailing ".0" spreadsheet artifact — but ONLY when what remains is
	// all digits (ut-docs#936 review finding F2). A genuine EAN/UPC/GTIN
	// is all-digit, so this recovers the real code; an alphanumeric
	// supplier code that legitimately ends ".0" ("ABC.0") must NOT be
	// truncated, or the catalog would silently store a different string
	// than the CSV said and a later scan of the literal code would never
	// resolve it. Scientific-notation mangling ("5.449E+12") is still not
	// un-mangled here (never was this function's job): it fails every
	// registry entry under a narrowed set, and under the DEFAULT set
	// CODE128's structural catch-all stores it verbatim — the deliberate
	// ADR-0059 §2 trade-off for the permissive catch-alls, escaped by
	// narrowing the enabled set, same as any other AddBarcode caller.
	if trimmed := strings.TrimSuffix(s, ".0"); trimmed != s && allDigits(trimmed) {
		s = trimmed
	}
	return barcode.Default().Match(enabledIDs, s)
}

// allDigits reports whether s is non-empty and every byte is '0'-'9'.
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func isTruthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "y", "yes", "true", "1":
		return true
	}
	return false
}
