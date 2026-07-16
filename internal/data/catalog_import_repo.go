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
