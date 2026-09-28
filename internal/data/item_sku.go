package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// ut-docs#3087: no catalog item may exist without a SKU. Every item insert
// (CreateItem, CreateItemTx — and through them the importer, the cloud/my.
// push, the catalog form, the API and SaveItem) fills a blank SKU from
// nextItemSKU, so the backfill and "Generate missing SKUs" follow-ups
// (ut-docs#3097, #3098) produce the same shape.
//
// The shape follows the shop's own pattern where one exists, and is always
// something a shop owner can read and type (never a UUID — ut-docs#1176):
//
//  1. The item's category already uses plain numeric SKUs (Kuchen
//     30089…30096) → the highest one + 1, same width (30097).
//  2. Otherwise → a category prefix plus a running number (KUC-0001), or
//     ITEM-0001 with no category / no usable Latin letters in its name.
//
// A candidate already used as an item or variant SKU, or as a barcode, is
// skipped: the sell screen resolves a scanned code by barcode and by SKU,
// so a generated SKU equal to another product's barcode would sell the
// wrong item.

const (
	// maxNumericSKUDigits caps the "follow the category's numbers" rule.
	// A longer all-digit SKU is almost always a GTIN (EAN-8/UPC-A/EAN-13/
	// GTIN-14) the shop keyed in as its SKU; +1 on one of those invents a
	// code that can belong to a real product's barcode. Such categories
	// fall back to the prefix scheme.
	maxNumericSKUDigits = 7
	// skuPrefixLen is how many letters/digits of the category name make
	// the prefix (Kuchen → KUC).
	skuPrefixLen = 3
	// skuSeqWidth zero-pads the prefix scheme's running number.
	skuSeqWidth = 4
	// defaultSKUPrefix is used with no category, or a category name with
	// too few Latin letters (e.g. a Persian or Greek name) to make one.
	defaultSKUPrefix = "ITEM"
	// maxSKUProbe bounds the walk past taken candidates; a gap this long
	// only happens with hand-made SKUs in the exact generated shape.
	maxSKUProbe = 1000
	// maxAutoSKUAttempts bounds CreateItem/CreateItemTx's retry when a
	// concurrent insert claims the generated SKU first.
	maxAutoSKUAttempts = 5
)

// skuQueryer is satisfied by *sql.DB and *sql.Tx.
type skuQueryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// isBlankSKU reports whether sku carries no real SKU.
func isBlankSKU(sku string) bool { return strings.TrimSpace(sku) == "" }

// nextItemSKU returns the SKU a new item in categoryID (nil or "" for none)
// should get. It reads through q so a caller inside a transaction sees its
// own earlier inserts.
func nextItemSKU(ctx context.Context, q skuQueryer, categoryID *string) (string, error) {
	cat := ""
	if categoryID != nil {
		cat = strings.TrimSpace(*categoryID)
	}
	if cat != "" {
		sku, ok, err := nextNumericCategorySKU(ctx, q, cat)
		if err != nil || ok {
			return sku, err
		}
	}
	prefix := defaultSKUPrefix
	if cat != "" {
		var name string
		err := q.QueryRowContext(ctx, `SELECT name FROM categories WHERE id = ?`, cat).Scan(&name)
		switch {
		case err == nil:
			prefix = skuPrefixFromName(name)
		case errors.Is(err, sql.ErrNoRows):
			// Unknown category id: the insert's own FK handling decides
			// what happens to it; the SKU just uses the default prefix.
		default:
			return "", fmt.Errorf("sku category name: %w", err)
		}
	}
	return nextPrefixedSKU(ctx, q, prefix)
}

// nextNumericCategorySKU applies rule 1. ok is false when the category has
// no plain numeric SKU (or the next one would outgrow maxNumericSKUDigits).
func nextNumericCategorySKU(ctx context.Context, q skuQueryer, categoryID string) (sku string, ok bool, err error) {
	rows, err := q.QueryContext(ctx, `
SELECT sku FROM items
WHERE category_id = ? AND sku IS NOT NULL AND sku <> ''
  AND sku NOT GLOB '*[^0-9]*' AND length(sku) <= ?`, categoryID, maxNumericSKUDigits)
	if err != nil {
		return "", false, fmt.Errorf("sku numeric pattern: %w", err)
	}
	defer rows.Close()
	var maxVal int64 = -1
	width := 0
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return "", false, fmt.Errorf("sku numeric pattern scan: %w", err)
		}
		v, perr := strconv.ParseInt(s, 10, 64)
		if perr != nil {
			continue
		}
		if v > maxVal || (v == maxVal && len(s) > width) {
			maxVal, width = v, len(s)
		}
	}
	if err := rows.Err(); err != nil {
		return "", false, fmt.Errorf("sku numeric pattern rows: %w", err)
	}
	if maxVal < 0 {
		return "", false, nil
	}
	for n := maxVal + 1; n <= maxVal+maxSKUProbe; n++ {
		cand := fmt.Sprintf("%0*d", width, n)
		if len(cand) > maxNumericSKUDigits {
			return "", false, nil
		}
		taken, err := skuTaken(ctx, q, cand)
		if err != nil {
			return "", false, err
		}
		if !taken {
			return cand, true, nil
		}
	}
	return "", false, nil
}

// nextPrefixedSKU applies rule 2: PREFIX-NNNN, one past the highest number
// already used with this prefix.
func nextPrefixedSKU(ctx context.Context, q skuQueryer, prefix string) (string, error) {
	lead := prefix + "-"
	// prefix is [A-Z0-9] only, so it needs no LIKE escaping; the GLOB keeps
	// only an all-digit remainder (LIKE alone is case-insensitive and would
	// also match ITEM-3F9A0C12, the old generated shape).
	rows, err := q.QueryContext(ctx, `
SELECT substr(sku, ?) FROM items
WHERE sku GLOB ? AND substr(sku, ?) NOT GLOB '*[^0-9]*' AND length(sku) BETWEEN ? AND ?`,
		len(lead)+1, lead+"[0-9]*", len(lead)+1, len(lead)+1, len(lead)+18)
	if err != nil {
		return "", fmt.Errorf("sku prefix sequence: %w", err)
	}
	defer rows.Close()
	var maxVal int64
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return "", fmt.Errorf("sku prefix sequence scan: %w", err)
		}
		if v, perr := strconv.ParseInt(s, 10, 64); perr == nil && v > maxVal {
			maxVal = v
		}
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("sku prefix sequence rows: %w", err)
	}
	for n := maxVal + 1; n <= maxVal+maxSKUProbe; n++ {
		cand := fmt.Sprintf("%s%0*d", lead, skuSeqWidth, n)
		taken, err := skuTaken(ctx, q, cand)
		if err != nil {
			return "", err
		}
		if !taken {
			return cand, nil
		}
	}
	return "", fmt.Errorf("sku prefix %s: no free number after %d", prefix, maxVal)
}

// skuTaken reports whether code is already an item/variant SKU or a
// barcode (see the file comment for why barcodes count).
func skuTaken(ctx context.Context, q skuQueryer, code string) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `
SELECT EXISTS(SELECT 1 FROM items WHERE sku = ?)
    OR EXISTS(SELECT 1 FROM item_variants WHERE sku = ?)
    OR EXISTS(SELECT 1 FROM item_barcodes WHERE barcode = ?)
    OR EXISTS(SELECT 1 FROM variant_barcodes WHERE barcode = ?)`,
		code, code, code, code).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("sku taken: %w", err)
	}
	return n != 0, nil
}

// skuPrefixFromName turns a category name into a SKU prefix: its first
// skuPrefixLen ASCII letters/digits, upper-cased, with accents stripped
// (Übergrößen → UBE, Café → CAF). A name yielding fewer than two
// characters, or no letter at all, falls back to defaultSKUPrefix.
func skuPrefixFromName(name string) string {
	var b strings.Builder
	letters := 0
	for _, r := range norm.NFD.String(name) {
		if b.Len() == skuPrefixLen {
			break
		}
		switch {
		case r == 'ß':
			b.WriteByte('S')
			letters++
		case r < unicode.MaxASCII && unicode.IsLetter(r):
			b.WriteRune(unicode.ToUpper(r))
			letters++
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		}
	}
	if b.Len() < 2 || letters == 0 {
		return defaultSKUPrefix
	}
	return b.String()
}
