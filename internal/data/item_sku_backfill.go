package data

import (
	"context"
	"database/sql"
	"fmt"
)

// ut-docs#3097: backfill for items created before ut-docs#3087 made every
// insert fill a blank SKU. The product owner's rule is that no catalog item
// may exist without a SKU, so every item — active or not — whose sku is
// NULL or blank gets one from nextItemSKU, the same generator the insert
// path uses. It runs once at boot on a primary/standalone till and from the
// Catalog page's "Generate missing SKUs" action.

// AssignedItemSKU is one item the backfill gives (or would give) a SKU.
type AssignedItemSKU struct {
	ItemID string
	Name   string
	SKU    string
}

// CountItemsMissingSKU returns how many items (active or not) have a NULL
// or blank SKU — the count on the Catalog page's "Generate missing SKUs"
// button.
func (r *CatalogRepo) CountItemsMissingSKU(ctx context.Context) (int, error) {
	var n int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM items WHERE sku IS NULL OR trim(sku) = ''`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count items missing sku: %w", err)
	}
	return n, nil
}

// PlanMissingItemSKUs returns the SKUs BackfillMissingItemSKUs would assign
// right now, without writing anything: it runs the very same routine in a
// transaction and rolls it back, so on unchanged data the preview equals
// the commit exactly.
func (r *CatalogRepo) PlanMissingItemSKUs(ctx context.Context) ([]AssignedItemSKU, error) {
	return r.assignMissingItemSKUs(ctx, false)
}

// BackfillMissingItemSKUs gives every item with a NULL or blank SKU a
// generated one, in one transaction, and returns what it assigned. A
// second run assigns nothing.
func (r *CatalogRepo) BackfillMissingItemSKUs(ctx context.Context) ([]AssignedItemSKU, error) {
	return r.assignMissingItemSKUs(ctx, true)
}

type missingSKUItem struct {
	id, name   string
	categoryID sql.NullString
}

func (r *CatalogRepo) assignMissingItemSKUs(ctx context.Context, commit bool) ([]AssignedItemSKU, error) {
	// The DSN's _txlock=immediate (ut-docs#311) makes this take the write
	// lock up front, so no concurrent insert can claim a SKU between
	// nextItemSKU's read and the UPDATE.
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("backfill item skus: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	items, err := itemsMissingSKU(ctx, tx)
	if err != nil {
		return nil, err
	}
	out := make([]AssignedItemSKU, 0, len(items))
	for _, it := range items {
		var cat *string
		if it.categoryID.Valid {
			cat = &it.categoryID.String
		}
		// Reading through tx means each item sees the SKUs assigned to the
		// ones before it, so two blank items never get the same SKU.
		sku, err := nextItemSKU(ctx, tx, cat, nil)
		if err != nil {
			return nil, fmt.Errorf("backfill item skus: generate for %s: %w", it.id, err)
		}
		res, err := tx.ExecContext(ctx, `UPDATE items SET sku = ?, updated_at = datetime('now') WHERE id = ? AND (sku IS NULL OR trim(sku) = '')`, sku, it.id)
		if err != nil {
			return nil, fmt.Errorf("backfill item skus: update %s: %w", it.id, err)
		}
		if n, err := res.RowsAffected(); err != nil {
			return nil, fmt.Errorf("backfill item skus: update %s: %w", it.id, err)
		} else if n == 0 {
			continue
		}
		out = append(out, AssignedItemSKU{ItemID: it.id, Name: it.name, SKU: sku})
	}
	if !commit {
		return out, nil // deferred Rollback discards the writes
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("backfill item skus: commit: %w", err)
	}
	return out, nil
}

// itemsMissingSKU lists every item with a NULL or blank SKU in a stable
// order (category, name, id), so a plan and a commit on the same data walk
// the items identically.
func itemsMissingSKU(ctx context.Context, tx *sql.Tx) ([]missingSKUItem, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT id, name, category_id FROM items
WHERE sku IS NULL OR trim(sku) = ''
ORDER BY category_id, name, id`)
	if err != nil {
		return nil, fmt.Errorf("backfill item skus: list: %w", err)
	}
	defer rows.Close()
	var items []missingSKUItem
	for rows.Next() {
		var it missingSKUItem
		if err := rows.Scan(&it.id, &it.name, &it.categoryID); err != nil {
			return nil, fmt.Errorf("backfill item skus: scan: %w", err)
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("backfill item skus: rows: %w", err)
	}
	return items, nil
}
