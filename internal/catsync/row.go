package catsync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

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
)

var barcodeRe = regexp.MustCompile(`^[0-9A-Za-z\-]{1,64}$`)

// wireRow is one ADR-0051 interchange item row as an import plugin submits
// it. Unknown fields are ignored (a later minor version may add some).
// There is deliberately no "source" field: core stamps the source.
type wireRow struct {
	ExternalID   string   `json:"external_id"`
	Name         string   `json:"name"`
	SKU          string   `json:"sku"`
	Barcode      string   `json:"barcode"`
	Barcodes     []string `json:"barcodes"`
	PriceMinor   *int64   `json:"price_minor"`
	CategoryPath []string `json:"category_path"`
	VATRateBP    *int64   `json:"vat_rate_bp"`
	StockQty     *float64 `json:"stock_qty"`
	Active       *bool    `json:"active"`
	SoldByWeight bool     `json:"sold_by_weight"`
}

// row is a validated wireRow.
type row struct {
	ext      string
	name     string
	sku      string
	barcodes []catalogtypes.BarcodeInput // normalized; ItemID unset
	price    money.Money
	catPath  []string // nil = leave the category as it is
	vat      int
	hasVAT   bool
	stock    float64
	hasStock bool
	active   bool
	weighed  bool
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

// parseRow decodes and validates one submitted row. ext is returned even
// when the row is otherwise invalid, so the issue can name it.
func parseRow(raw json.RawMessage) (row, string, string) {
	var w wireRow
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&w); err != nil {
		return row{}, "", "row is not a valid item object"
	}
	if dec.More() {
		return row{}, "", "row is not a single JSON object"
	}
	ext, ok := cleanText(w.ExternalID, externalIDMax)
	if !ok {
		return row{}, "", "external_id is missing or invalid"
	}
	r := row{ext: ext, active: true, weighed: w.SoldByWeight}
	if r.name, ok = cleanText(w.Name, nameMaxRunes); !ok {
		return row{}, ext, "name is missing or invalid"
	}
	if w.PriceMinor == nil || *w.PriceMinor < 0 || *w.PriceMinor > priceMinorMax {
		return row{}, ext, "price_minor is missing or out of range"
	}
	r.price = money.FromMinor(*w.PriceMinor)
	if strings.TrimSpace(w.SKU) != "" {
		if r.sku, ok = cleanText(w.SKU, skuMax); !ok {
			return row{}, ext, "sku is invalid"
		}
	}
	if w.CategoryPath != nil {
		if len(w.CategoryPath) > categoryDepth {
			return row{}, ext, fmt.Sprintf("category_path is deeper than %d levels", categoryDepth)
		}
		for _, c := range w.CategoryPath {
			name, ok := cleanText(c, categoryMaxRune)
			if !ok {
				return row{}, ext, "category_path has an empty or invalid level"
			}
			r.catPath = append(r.catPath, name)
		}
		if r.catPath == nil {
			r.catPath = []string{} // explicit empty path = no category
		}
	}
	if w.VATRateBP != nil {
		if *w.VATRateBP < 0 || *w.VATRateBP > vatMaxBP {
			return row{}, ext, "vat_rate_bp is out of range (0-10000)"
		}
		r.vat, r.hasVAT = int(*w.VATRateBP), true
	}
	if w.StockQty != nil {
		q := *w.StockQty
		if math.IsNaN(q) || math.IsInf(q, 0) || math.Abs(q) > stockMaxAbs {
			return row{}, ext, "stock_qty is out of range"
		}
		r.stock, r.hasStock = q, true
	}
	if w.Active != nil {
		r.active = *w.Active
	}
	codes := w.Barcodes
	if b := strings.TrimSpace(w.Barcode); b != "" {
		codes = append([]string{b}, codes...)
	}
	if len(codes) > barcodesMax {
		return row{}, ext, fmt.Sprintf("more than %d barcodes", barcodesMax)
	}
	seen := map[string]bool{}
	for _, c := range codes {
		c = strings.TrimSpace(c)
		if !barcodeRe.MatchString(c) {
			return row{}, ext, "a barcode is invalid"
		}
		if seen[c] {
			continue
		}
		seen[c] = true
		r.barcodes = append(r.barcodes, catalogtypes.BarcodeInput{Barcode: c})
	}
	return r, ext, ""
}

// normalizeBarcodes runs the shop's symbology rules over r's barcodes
// (through the pool — it reads settings, so it runs before the commit's
// transaction opens).
func normalizeBarcodes(ctx context.Context, cat *data.CatalogRepo, r *row) string {
	out := r.barcodes[:0]
	seen := map[string]bool{}
	for i, b := range r.barcodes {
		in, err := cat.NormalizeBarcode(ctx, catalogtypes.BarcodeInput{Barcode: b.Barcode, ItemID: "pending", IsPrimary: i == 0})
		if err != nil {
			return "barcode " + b.Barcode + " is not accepted by this till's barcode settings"
		}
		if seen[in.Barcode] {
			continue
		}
		seen[in.Barcode] = true
		in.ItemID = ""
		out = append(out, in)
	}
	r.barcodes = out
	return ""
}
