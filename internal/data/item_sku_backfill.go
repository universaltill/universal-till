package data

import (
	"context"
	"database/sql"
	"fmt"
)

// ut-docs#3097: backfill for items created before ut-docs#3087 made every
// insert fill a blank SKU. The product owner's rule is that no catalog item
// may exist without a SKU, so every item — active or not — whose sku is
// NULL or blank gets one by nextItemSKU's rules, the generator the insert
// path uses (applied in bulk by skuPlanner, ut-docs#3280). It runs once at boot on a primary/standalone till and from the
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
// right now, without writing anything. It runs the same planner inside a
// read-only transaction (ut-docs#3280): a consistent snapshot that takes no
// write lock, so a preview never blocks a sale. On unchanged data it equals
// the commit exactly.
func (r *CatalogRepo) PlanMissingItemSKUs(ctx context.Context) ([]AssignedItemSKU, error) {
	// ReadOnly makes the driver issue a plain deferred BEGIN instead of
	// the DSN's _txlock=immediate.
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("plan item skus: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	return planMissingItemSKUs(ctx, tx, nil)
}

// BackfillMissingItemSKUs gives every item with a NULL or blank SKU a
// generated one, in one transaction, and returns what it assigned. A
// second run assigns nothing.
func (r *CatalogRepo) BackfillMissingItemSKUs(ctx context.Context) ([]AssignedItemSKU, error) {
	// The DSN's _txlock=immediate (ut-docs#311) makes this take the write
	// lock up front, so no concurrent insert can claim a SKU between the
	// planner's reads and the UPDATEs.
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("backfill item skus: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, `UPDATE items SET sku = ?, updated_at = datetime('now') WHERE id = ? AND (sku IS NULL OR trim(sku) = '')`)
	if err != nil {
		return nil, fmt.Errorf("backfill item skus: prepare: %w", err)
	}
	defer stmt.Close()
	out, err := planMissingItemSKUs(ctx, tx, func(id, sku string) (bool, error) {
		res, err := stmt.ExecContext(ctx, sku, id)
		if err != nil {
			return false, fmt.Errorf("backfill item skus: update %s: %w", id, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return false, fmt.Errorf("backfill item skus: update %s: %w", id, err)
		}
		return n != 0, nil
	})
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("backfill item skus: commit: %w", err)
	}
	return out, nil
}

type missingSKUItem struct {
	id, name   string
	categoryID sql.NullString
}

// planMissingItemSKUs plans a SKU for every blank item in one pass over an
// in-memory skuPlanner (ut-docs#3280). apply, when non-nil, stores each
// SKU and reports whether the row still needed it; a SKU it did not store
// is neither returned nor treated as taken.
func planMissingItemSKUs(ctx context.Context, tx *sql.Tx, apply func(id, sku string) (bool, error)) ([]AssignedItemSKU, error) {
	items, err := itemsMissingSKU(ctx, tx)
	if err != nil {
		return nil, err
	}
	out := make([]AssignedItemSKU, 0, len(items))
	if len(items) == 0 {
		return out, nil
	}
	planner, err := loadSKUPlanner(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("backfill item skus: %w", err)
	}
	for _, it := range items {
		var cat *string
		if it.categoryID.Valid {
			cat = &it.categoryID.String
		}
		sku, err := planner.next(ctx, cat)
		if err != nil {
			return nil, fmt.Errorf("backfill item skus: generate for %s: %w", it.id, err)
		}
		if apply != nil {
			stored, err := apply(it.id, sku)
			if err != nil {
				return nil, err
			}
			if !stored {
				continue
			}
		}
		// Each item sees the SKUs planned before it, so two blank items
		// never get the same SKU.
		planner.take(cat, sku)
		out = append(out, AssignedItemSKU{ItemID: it.id, Name: it.name, SKU: sku})
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
