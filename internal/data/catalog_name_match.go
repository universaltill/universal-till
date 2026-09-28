package data

import (
	"context"
	"fmt"
	"strings"
)

// FoldItemName is the key ActiveItemIDsByName matches on: trimmed, Unicode
// case-folded (strings.ToLower, not SQLite's ASCII-only lower()/NOCASE, so
// "Çay" and "çay" meet).
func FoldItemName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// ActiveItemIDsByName (ut-docs#2703, reopened) maps each wanted name's
// FoldItemName key to the ids of the ACTIVE items whose name folds to the
// same key -- an exact match, never the sale screen's name-LIKE search. A
// name with no entry matched nothing; one with several ids is ambiguous and
// the caller decides (the legacy counter-order conversion refuses to guess).
// Folding happens in Go, so the scan is one pass over active item names.
func (r *CatalogRepo) ActiveItemIDsByName(ctx context.Context, names []string) (map[string][]string, error) {
	want := make(map[string]bool, len(names))
	for _, n := range names {
		if k := FoldItemName(n); k != "" {
			want[k] = true
		}
	}
	out := map[string][]string{}
	if len(want) == 0 {
		return out, nil
	}
	rows, err := r.db.QueryContext(ctx, `SELECT id, name FROM items WHERE is_active = 1`)
	if err != nil {
		return nil, fmt.Errorf("match items by name: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, fmt.Errorf("match items by name: scan: %w", err)
		}
		if k := FoldItemName(name); want[k] {
			out[k] = append(out[k], id)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("match items by name: iterate: %w", err)
	}
	return out, nil
}

// ItemIDCode is the code the price resolver answers for a catalog item by
// its id ("item:<id>", itemIDCodePrefix, ut-docs#2294) -- the item's
// current price and tax, whether or not it has a barcode, SKU or button.
func ItemIDCode(itemID string) string { return itemIDCodePrefix + itemID }
