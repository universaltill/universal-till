package catimport

import (
	"strings"
	"unicode/utf8"

	"github.com/universaltill/universal-till/catimport/catalogrules"
)

// Row is one ADR-0051 interchange item row: what an import plugin submits
// through import_rows_submit (ADR-0161 §2) and what ToRows emits for a
// file this module parsed (ADR-0164 §1). The till's catsync decodes
// plugin rows straight into this type, so the vocabulary is defined once.
// There is deliberately no "source" field: the consumer stamps the source.
// Money is integer minor units; a rate is basis points.
type Row struct {
	ExternalID string `json:"external_id"`
	Name       string `json:"name"`
	SKU        string `json:"sku"`
	Barcode    string `json:"barcode"`
	// BarcodeType is the catimport/barcode registry id Barcode was decoded
	// with (ADR-0059), when the producer decoded it: Barcode is then that
	// decode's lookup key (an embedded-data label's zeroed template), and a
	// consumer stores it as typed instead of re-matching the key, which
	// would decode it twice (ut-docs#936). Empty = untyped: the consumer
	// matches Barcode against the shop's symbologies. It types Barcode
	// only, never Barcodes. Additive under ADR-0051 §3.
	BarcodeType  string   `json:"barcode_type,omitempty"`
	Barcodes     []string `json:"barcodes"`
	PriceMinor   *int64   `json:"price_minor"`
	CategoryPath []string `json:"category_path"` // nil = leave the category as it is
	VATRateBP    *int64   `json:"vat_rate_bp"`
	StockQty     *float64 `json:"stock_qty"`
	Active       *bool    `json:"active"`
	SoldByWeight bool     `json:"sold_by_weight"`
}

// ExternalIDMaxRunes is ADR-0161 §3's bound on external_id; a longer
// source id would be refused by every consumer.
const ExternalIDMaxRunes = 128

// ToRows turns a parsed file into ADR-0051 rows, in file order, skipping
// every item RowFor refuses. A caller that reports per item (a preview, a
// row report) calls RowFor on res.Items itself.
func ToRows(res Result) []Row {
	out := make([]Row, 0, len(res.Items))
	for _, it := range res.Items {
		if r, ok := RowFor(it); ok {
			out = append(out, r)
		}
	}
	return out
}

// RowFor maps one item to its row. ok is false for an item that can't be a
// row:
//   - it carries a blocking Issue (missing name, bad price, ...);
//   - it isn't a standard item (a gift voucher): ADR-0051 rows have no item
//     type, and a voucher must never land as an ordinary 0-priced product.
//
// The non-blocking issues (a dropped barcode, an unparseable tax rate, a
// de-duplicated SKU) don't stop a row — the field is simply absent, as the
// till's own importer treats them. The SKU is never invented here: a row
// without one stays without one, and the consumer generates it in the
// till's PreviewItemSKU format (#1459, ADR-0164 §3), so it can count what
// it generated.
func RowFor(it ImportItem) (Row, bool) {
	if it.Issue != "" {
		return Row{}, false
	}
	if it.ItemType != "" && it.ItemType != catalogrules.ItemTypeStandard {
		return Row{}, false
	}
	price := it.PriceMinor
	r := Row{
		Name:         it.Name,
		SKU:          it.SKU,
		Barcode:      it.Barcode,
		BarcodeType:  it.BarcodeType,
		PriceMinor:   &price,
		SoldByWeight: it.IsWeighed,
	}
	if utf8.RuneCountInString(it.ExternalID) <= ExternalIDMaxRunes {
		r.ExternalID = it.ExternalID
	}
	// Department is the axis categories nest under (an ERP master), so it
	// is the path's first level.
	for _, level := range []string{it.Department, it.Category} {
		if level = strings.TrimSpace(level); level != "" {
			r.CategoryPath = append(r.CategoryPath, level)
		}
	}
	if it.HasTax {
		bp := int64(it.TaxRateBP)
		r.VATRateBP = &bp
	}
	// A source that says it doesn't track the item still writes a quantity
	// for it; that number is not a stock level (ut-docs#1843).
	if it.HasStock && (!it.HasTracksStock || it.TracksStock) {
		q := it.Stock
		r.StockQty = &q
	}
	return r, true
}

// sourceExternalID reads the row's stable source id for the formats that
// have one (ADR-0164 §1); get returns a field's trimmed cell value.
func sourceExternalID(format string, get func(field string) string) string {
	switch format {
	case FormatLoyverse:
		// Variants share a Handle, so the SKU qualifies it on every row
		// that has one — never only when the Handle repeats in this file,
		// or adding a variant in a later export would change the ids of
		// the items already imported (ADR-0164 re-import idempotency).
		return joinExternalID(get("handle"), get("sku"))
	case FormatSquare:
		return get("token")
	case FormatSumUp:
		return joinExternalID(get("item_id"), get("variant_id"))
	case FormatShopify:
		return joinExternalID(get("handle"), get("variant_sku"))
	}
	return ""
}

// joinExternalID qualifies base with qual ("tee/SH-TEE-S"); no base, no id;
// no qual, the bare base. The id depends on the row alone, so it is the
// same in every export. Two rows with the same base and no (or the same)
// qual get the same id: the consumer reports the repeat as a row issue
// (catsync's external_id_duplicate), never a guess.
func joinExternalID(base, qual string) string {
	switch {
	case base == "":
		return ""
	case qual == "":
		return base
	}
	return base + "/" + qual
}
