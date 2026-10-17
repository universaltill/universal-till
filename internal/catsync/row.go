package catsync

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/universaltill/universal-till/catimport"
	"github.com/universaltill/universal-till/catimport/barcode"
	"github.com/universaltill/universal-till/catimport/catalogrules"

	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/money"
)

// Row field bounds. A row outside them is a per-row issue, never a failed
// batch (ADR-0161 §3).
const (
	externalIDMax   = 128
	nameMaxRunes    = 200
	skuMax          = 64
	barcodesMax     = 10
	categoryDepth   = 4
	categoryMaxRune = 100
	priceMinorMax   = 1_000_000_000_00 // a sanity ceiling, not a business rule
	vatMaxBP        = 10_000
	stockMaxAbs     = 1_000_000_000
	// descriptionMaxRunes is the description bound the cloud catalogue
	// applies (ut-cloud catalogwrite/rules.MaxDescriptionRunes); the till's
	// own editor and file import set none.
	descriptionMaxRunes = 1000
)

var barcodeRe = regexp.MustCompile(`^[0-9A-Za-z\-]{1,64}$`)

// wireRow is one ADR-0051 interchange item row as an import plugin submits
// it. The type is the shared catimport module's Row (ADR-0164 §1), so the
// module's ToRows and a plugin's rows share one vocabulary. Unknown fields
// are ignored (a later minor version may add some). There is deliberately
// no "source" field: core stamps the source.
type wireRow = catimport.Row

// row is a validated wireRow.
type row struct {
	ext      string
	name     string
	sku      string
	barcodes []catalogtypes.BarcodeInput // normalized; ItemID unset; BarcodeType set only when the row typed it
	price    money.Money
	catPath  []string // nil = leave the category as it is
	vat      int
	hasVAT   bool
	stock    float64
	hasStock bool
	active   bool
	weighed  bool
	desc     string // "" = leave the item's description as it is
	age      *bool  // nil = leave the item's age check as it is
	// nqValue/nqUnit: a pair that passed catalogrules.ValidNetQuantity;
	// both nil = leave the item's net quantity as it is.
	nqValue *int64
	nqUnit  *string
}

// cleanText trims s and refuses empty, over-long, invalid UTF-8, control
// and invisible format characters (the agentaction rule: a plugin must not
// show the operator a different name than the one stored).
func cleanText(s string, maxRunes int) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || !utf8.ValidString(s) || utf8.RuneCountInString(s) > maxRunes {
		return "", false
	}
	for _, r := range s {
		// Format runs are rejected (bidi overrides could show the operator
		// a different name), except ZWNJ/ZWJ, which Persian/Urdu spelling
		// and emoji sequences need.
		if unicode.IsControl(r) || (unicode.Is(unicode.Cf, r) && r != '\u200c' && r != '\u200d') {
			return "", false
		}
	}
	return s, true
}

// cleanDescription is cleanText for a description: blank is fine (it means
// "leave as is"), and line breaks and tabs are allowed, since a
// description is multi-line text (the file import keeps them too). Every
// other control or invisible format character is refused, as in names.
func cleanDescription(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > descriptionMaxRunes {
		return "", false
	}
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' {
			continue
		}
		if unicode.IsControl(r) || (unicode.Is(unicode.Cf, r) && r != '\u200c' && r != '\u200d') {
			return "", false
		}
	}
	return s, true
}

// parseRow decodes and validates one submitted row. ext is returned even
// when the row is otherwise invalid, so the issue can name it.
func parseRow(raw json.RawMessage) (row, string, rowIssue) {
	var w wireRow
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&w); err != nil {
		return row{}, "", newIssue(IssueRowNotObject)
	}
	if dec.More() {
		return row{}, "", newIssue(IssueRowNotSingleObject)
	}
	ext, ok := cleanText(w.ExternalID, externalIDMax)
	if !ok {
		return row{}, "", newIssue(IssueExternalIDInvalid)
	}
	r := row{ext: ext, active: true, weighed: w.SoldByWeight}
	if r.name, ok = cleanText(w.Name, nameMaxRunes); !ok {
		return row{}, ext, newIssue(IssueNameInvalid)
	}
	if w.PriceMinor == nil || *w.PriceMinor < 0 || *w.PriceMinor > priceMinorMax {
		return row{}, ext, newIssue(IssuePriceInvalid)
	}
	r.price = money.FromMinor(*w.PriceMinor)
	if strings.TrimSpace(w.SKU) != "" {
		if r.sku, ok = cleanText(w.SKU, skuMax); !ok {
			return row{}, ext, newIssue(IssueSKUInvalid)
		}
	}
	if w.CategoryPath != nil {
		if len(w.CategoryPath) > categoryDepth {
			return row{}, ext, newIssue(IssueCategoryTooDeep, strconv.Itoa(categoryDepth))
		}
		for _, c := range w.CategoryPath {
			name, ok := cleanText(c, categoryMaxRune)
			if !ok {
				return row{}, ext, newIssue(IssueCategoryLevelInvalid)
			}
			r.catPath = append(r.catPath, name)
		}
		if r.catPath == nil {
			r.catPath = []string{} // explicit empty path = no category
		}
	}
	if w.VATRateBP != nil {
		if *w.VATRateBP < 0 || *w.VATRateBP > vatMaxBP {
			return row{}, ext, newIssue(IssueVATOutOfRange)
		}
		r.vat, r.hasVAT = int(*w.VATRateBP), true
	}
	if w.StockQty != nil {
		q := *w.StockQty
		if math.IsNaN(q) || math.IsInf(q, 0) || math.Abs(q) > stockMaxAbs {
			return row{}, ext, newIssue(IssueStockOutOfRange)
		}
		r.stock, r.hasStock = q, true
	}
	if w.Active != nil {
		r.active = *w.Active
	}
	if r.desc, ok = cleanDescription(w.Description); !ok {
		return row{}, ext, newIssue(IssueDescriptionInvalid)
	}
	r.age = w.AgeRestricted
	if w.NetQuantityValue != nil || w.NetQuantityUnit != nil {
		if !catalogrules.ValidNetQuantity(w.NetQuantityValue, w.NetQuantityUnit) {
			return row{}, ext, newIssue(IssueNetQuantityInvalid)
		}
		r.nqValue, r.nqUnit = w.NetQuantityValue, w.NetQuantityUnit
	}
	codes := w.Barcodes
	b := strings.TrimSpace(w.Barcode)
	if b != "" {
		codes = append([]string{b}, codes...)
	}
	// A typed barcode (barcode_type) is its producer's decode: barcode is
	// that type's lookup key, stored as typed, never re-matched (#936). It
	// types barcode only, and must be exactly that type's key.
	typ := strings.ToUpper(strings.TrimSpace(w.BarcodeType))
	if typ != "" && (b == "" || !barcode.Default().IsLookupKey(typ, b)) {
		return row{}, ext, newIssue(IssueBarcodeInvalid)
	}
	if len(codes) > barcodesMax {
		return row{}, ext, newIssue(IssueTooManyBarcodes, strconv.Itoa(barcodesMax))
	}
	seen := map[string]bool{}
	for _, c := range codes {
		c = strings.TrimSpace(c)
		if !barcodeRe.MatchString(c) {
			return row{}, ext, newIssue(IssueBarcodeInvalid)
		}
		if seen[c] {
			continue
		}
		seen[c] = true
		in := catalogtypes.BarcodeInput{Barcode: c}
		if c == b {
			in.BarcodeType = typ
		}
		r.barcodes = append(r.barcodes, in)
	}
	return r, ext, rowIssue{}
}

// normalizeBarcodes runs the shop's symbology rules over r's barcodes
// (through the pool — it reads settings, so it runs before the commit's
// transaction opens). A typed barcode is checked against its type and
// stored as is; an untyped one is matched against the enabled symbologies.
func normalizeBarcodes(ctx context.Context, cat *data.CatalogRepo, r *row) rowIssue {
	out := r.barcodes[:0]
	seen := map[string]bool{}
	for i, b := range r.barcodes {
		normalize := cat.NormalizeBarcode
		if b.BarcodeType != "" {
			normalize = cat.NormalizeTypedBarcode
		}
		in, err := normalize(ctx, catalogtypes.BarcodeInput{Barcode: b.Barcode, BarcodeType: b.BarcodeType, ItemID: "pending", IsPrimary: i == 0})
		if err != nil {
			return newIssue(IssueBarcodeNotAccepted, b.Barcode)
		}
		if seen[in.Barcode] {
			continue
		}
		seen[in.Barcode] = true
		in.ItemID = ""
		out = append(out, in)
	}
	r.barcodes = out
	return rowIssue{}
}
