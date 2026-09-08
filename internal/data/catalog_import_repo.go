package data

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// EnsureCategoryUnder returns the id of a category by name scoped to a parent,
// creating it if missing. An empty parentID means a top-level category — a
// department, in enterprise/ERP imports (docs/arch/enterprise-department-stores.md:
// departments are top-level categories that sub-categories nest under). The
// lookup is parent-scoped and case-insensitive so re-running an import is
// idempotent: the same department + category resolve to the same rows.
func (r *CatalogRepo) EnsureCategoryUnder(ctx context.Context, name, parentID string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", nil
	}
	var (
		id  string
		err error
	)
	if parentID == "" {
		err = r.db.QueryRowContext(ctx,
			`SELECT id FROM categories WHERE name = ? COLLATE NOCASE AND parent_id IS NULL`,
			name).Scan(&id)
	} else {
		err = r.db.QueryRowContext(ctx,
			`SELECT id FROM categories WHERE name = ? COLLATE NOCASE AND parent_id = ?`,
			name, parentID).Scan(&id)
	}
	if err == nil {
		return id, nil
	}
	if err != sql.ErrNoRows {
		return "", fmt.Errorf("find category: %w", err)
	}
	id = uuid.NewString()
	if _, err := r.db.ExecContext(ctx,
		`INSERT INTO categories (id, name, parent_id) VALUES (?, ?, ?)`,
		id, name, nullableString(parentID)); err != nil {
		return "", fmt.Errorf("create category: %w", err)
	}
	return id, nil
}

// findCategoryIDUnder is EnsureCategoryUnder's read-only half: the same
// parent-scoped, case-insensitive lookup, but never creates a missing row.
// Used by ItemExistsByNameAndCategory below, which must not have the side
// effect of creating a category just to check whether an item exists in it.
func (r *CatalogRepo) findCategoryIDUnder(ctx context.Context, name, parentID string) (id string, ok bool, err error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", false, nil
	}
	if parentID == "" {
		err = r.db.QueryRowContext(ctx,
			`SELECT id FROM categories WHERE name = ? COLLATE NOCASE AND parent_id IS NULL`,
			name).Scan(&id)
	} else {
		err = r.db.QueryRowContext(ctx,
			`SELECT id FROM categories WHERE name = ? COLLATE NOCASE AND parent_id = ?`,
			name, parentID).Scan(&id)
	}
	if err == nil {
		return id, true, nil
	}
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	return "", false, fmt.Errorf("find category under: %w", err)
}

// ItemExistsByNameAndCategory is the import dedupe fallback for a row that
// carries neither a SKU nor a barcode (ut-docs#1839) — a real SumUp café
// export is exactly this shape (both columns optional, empty by default),
// so without this, re-importing the same file duplicated 114 of 116 items:
// BarcodeExists/SKUExists are never even reached for a codeless row.
//
// Matches on (name, department, category) with the same normalisation
// EnsureCategoryUnder already applies (trim + COLLATE NOCASE) and the same
// department/category nesting import commit uses (department = a top-level
// category, the row's category nests under it). Deliberately scoped by
// category, not name alone: a café can legitimately sell two different
// things that happen to share a name in different categories, and this must
// not block that (see the card's acceptance criteria). The trade-off this
// accepts — two genuinely different items sharing both a name AND a
// category collide — mirrors the one BarcodeExists/SKUExists already make
// for a shared code, so it isn't a new kind of imprecision.
//
// Read-only: department/category names that don't exist yet resolve to "no
// match" rather than being created (EnsureCategoryUnder's side effect would
// be wrong here — this is a check, not a commit).
func (r *CatalogRepo) ItemExistsByNameAndCategory(ctx context.Context, name, department, category string) (bool, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return false, nil
	}

	var catID *string
	if department != "" {
		deptID, ok, err := r.findCategoryIDUnder(ctx, department, "")
		if err != nil {
			return false, err
		}
		if !ok {
			// The department itself has never been imported, so nothing
			// could already be filed under it.
			return false, nil
		}
		if category != "" {
			id, ok, err := r.findCategoryIDUnder(ctx, category, deptID)
			if err != nil {
				return false, err
			}
			if !ok {
				return false, nil
			}
			catID = &id
		} else {
			catID = &deptID
		}
	} else if category != "" {
		id, ok, err := r.findCategoryIDUnder(ctx, category, "")
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
		catID = &id
	}

	var n int
	var err error
	if catID == nil {
		err = r.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM items WHERE name = ? COLLATE NOCASE AND category_id IS NULL`,
			name).Scan(&n)
	} else {
		err = r.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM items WHERE name = ? COLLATE NOCASE AND category_id = ?`,
			name, *catID).Scan(&n)
	}
	if err != nil {
		return false, fmt.Errorf("item exists by name/category: %w", err)
	}
	return n > 0, nil
}
