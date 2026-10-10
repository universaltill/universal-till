package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ut-docs#3280: skuPlanner is nextItemSKU's two rules over an in-memory
// copy of the codes, for the bulk backfill. Calling nextItemSKU once per
// blank item rescanned the category and the prefix range and probed four
// tables per candidate — O(N·C + N²) index visits inside the write lock.
// The planner reads each table once, then generates every SKU in memory.
//
// It must assign exactly what repeated nextItemSKU calls would (pinned by
// TestSKUPlan_MatchesPerItemGenerator): same rules, same order, and take
// records each assignment so the next item sees it, the way a later
// nextItemSKU call saw the earlier UPDATE.
type skuPlanner struct {
	// taken: every item SKU, variant SKU and barcode (skuTaken's tables).
	// Codes are compared as Go strings, so a BLOB-typed code (no app path
	// writes one) counts as taken here but not in skuTaken's `=`.
	taken map[string]bool
	// byCategory: item SKUs per stored category id, for rule 1's lazy
	// per-category maximum.
	byCategory map[string][]string
	numeric    map[string]numericMax // category id → rule 1 state
	// prefix: highest number per PREFIX-NNNN prefix, built in one pass.
	// Prefixes contain no '-', so a SKU's candidate prefix is everything
	// before its first '-'.
	prefix  map[string]int64
	catName map[string]string // category id → prefix
	q       skuQueryer
}

type numericMax struct {
	max   int64 // -1: the category has no plain numeric SKU
	width int
}

func loadSKUPlanner(ctx context.Context, q skuQueryer) (*skuPlanner, error) {
	p := &skuPlanner{
		taken:      map[string]bool{},
		byCategory: map[string][]string{},
		numeric:    map[string]numericMax{},
		prefix:     map[string]int64{},
		catName:    map[string]string{},
		q:          q,
	}
	rows, err := q.QueryContext(ctx, `SELECT sku, COALESCE(category_id, '') FROM items WHERE sku IS NOT NULL`)
	if err != nil {
		return nil, fmt.Errorf("sku plan: items: %w", err)
	}
	for rows.Next() {
		var sku, cat string
		if err := rows.Scan(&sku, &cat); err != nil {
			rows.Close()
			return nil, fmt.Errorf("sku plan: items scan: %w", err)
		}
		p.recordItemSKU(cat, sku)
	}
	if err := closeRows(rows); err != nil {
		return nil, fmt.Errorf("sku plan: items rows: %w", err)
	}
	for _, src := range []string{
		`SELECT sku FROM item_variants WHERE sku IS NOT NULL`,
		`SELECT barcode FROM item_barcodes WHERE barcode IS NOT NULL`,
		`SELECT barcode FROM variant_barcodes WHERE barcode IS NOT NULL`,
	} {
		rows, err := q.QueryContext(ctx, src)
		if err != nil {
			return nil, fmt.Errorf("sku plan: taken codes: %w", err)
		}
		for rows.Next() {
			var code string
			if err := rows.Scan(&code); err != nil {
				rows.Close()
				return nil, fmt.Errorf("sku plan: taken codes scan: %w", err)
			}
			p.taken[code] = true
		}
		if err := closeRows(rows); err != nil {
			return nil, fmt.Errorf("sku plan: taken codes rows: %w", err)
		}
	}
	return p, nil
}

func closeRows(rows *sql.Rows) error {
	err := rows.Err()
	if cerr := rows.Close(); err == nil {
		err = cerr
	}
	return err
}

// isPlainNumericSKU mirrors nextNumericCategorySKU's filter: non-empty,
// digits only, at most maxNumericSKUDigits long.
func isPlainNumericSKU(s string) bool {
	if s == "" || len(s) > maxNumericSKUDigits {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// prefixNumber mirrors nextPrefixedSKU's filter: lead followed by 1–18
// digits and nothing else (case-sensitive, like GLOB).
func prefixNumber(sku, lead string) (int64, bool) {
	if !strings.HasPrefix(sku, lead) {
		return 0, false
	}
	rest := sku[len(lead):]
	if rest == "" || len(rest) > 18 {
		return 0, false
	}
	for i := 0; i < len(rest); i++ {
		if rest[i] < '0' || rest[i] > '9' {
			return 0, false
		}
	}
	v, err := strconv.ParseInt(rest, 10, 64)
	return v, err == nil
}

func (p *skuPlanner) numericState(categoryID string) numericMax {
	if st, ok := p.numeric[categoryID]; ok {
		return st
	}
	st := numericMax{max: -1}
	for _, sku := range p.byCategory[categoryID] {
		if !isPlainNumericSKU(sku) {
			continue
		}
		v, err := strconv.ParseInt(sku, 10, 64)
		if err != nil {
			continue
		}
		if v > st.max || (v == st.max && len(sku) > st.width) {
			st.max, st.width = v, len(sku)
		}
	}
	p.numeric[categoryID] = st
	return st
}

// recordItemSKU adds one items.sku (stored category id, "" = none) to the
// taken set, its category's list and the prefix maxima.
func (p *skuPlanner) recordItemSKU(categoryID, sku string) {
	p.taken[sku] = true
	p.byCategory[categoryID] = append(p.byCategory[categoryID], sku)
	if i := strings.IndexByte(sku, '-'); i > 0 {
		pf := sku[:i]
		if v, ok := prefixNumber(sku, pf+"-"); ok && v > p.prefix[pf] {
			p.prefix[pf] = v
		}
	}
}

func (p *skuPlanner) prefixFor(ctx context.Context, categoryID string) (string, error) {
	if categoryID == "" {
		return defaultSKUPrefix, nil
	}
	if pf, ok := p.catName[categoryID]; ok {
		return pf, nil
	}
	pf := defaultSKUPrefix
	var name string
	err := p.q.QueryRowContext(ctx, `SELECT name FROM categories WHERE id = ?`, categoryID).Scan(&name)
	switch {
	case err == nil:
		pf = skuPrefixFromName(name)
	case errors.Is(err, sql.ErrNoRows):
		// Same as nextItemSKU: an unknown category id gets the default.
	default:
		return "", fmt.Errorf("sku category name: %w", err)
	}
	p.catName[categoryID] = pf
	return pf, nil
}

// next returns the SKU nextItemSKU would give a new item in categoryID
// (nil or blank for none) given everything taken so far. It changes
// nothing; take records the assignment.
func (p *skuPlanner) next(ctx context.Context, categoryID *string) (string, error) {
	cat := ""
	if categoryID != nil {
		cat = strings.TrimSpace(*categoryID)
	}
	if cat != "" {
		if st := p.numericState(cat); st.max >= 0 {
			for n := st.max + 1; n <= st.max+maxSKUProbe; n++ {
				cand := fmt.Sprintf("%0*d", st.width, n)
				if len(cand) > maxNumericSKUDigits {
					break
				}
				if !p.taken[cand] {
					return cand, nil
				}
			}
		}
	}
	prefix, err := p.prefixFor(ctx, cat)
	if err != nil {
		return "", err
	}
	maxVal := p.prefix[prefix]
	lead := prefix + "-"
	for n := maxVal + 1; n <= maxVal+maxSKUProbe; n++ {
		cand := fmt.Sprintf("%s%0*d", lead, skuSeqWidth, n)
		if !p.taken[cand] {
			return cand, nil
		}
	}
	return "", fmt.Errorf("sku prefix %s: no free number after %d", prefix, maxVal)
}

// take records that an item in categoryID (as stored: nil = none) now
// holds sku. Moving the maxima is what keeps next O(1): without it the
// probe walks past every earlier assignment and gives up after
// maxSKUProbe. A planned SKU is always above its sequence's max, so there
// is no width tie.
//
// The numeric cache is keyed by the stored id while next trims: that
// matches nextNumericCategorySKU's `category_id = ?` with a trimmed
// argument, which never sees a padded id's rows. Don't "fix" it.
func (p *skuPlanner) take(categoryID *string, sku string) {
	cat := ""
	if categoryID != nil {
		cat = *categoryID
	}
	p.recordItemSKU(cat, sku)
	if st, ok := p.numeric[cat]; ok && isPlainNumericSKU(sku) {
		if v, err := strconv.ParseInt(sku, 10, 64); err == nil && v > st.max {
			p.numeric[cat] = numericMax{max: v, width: len(sku)}
		}
	}
}
