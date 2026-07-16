package data_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
)

// EnsureCategoryUnder underpins enterprise import: a department is a top-level
// category and the item's category nests under it. Re-running an import must
// resolve to the same rows (idempotent), and a category name reused under two
// departments must produce two distinct rows (parent-scoped).
func TestEnsureCategoryUnder_DepartmentNestingIsIdempotent(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "cat.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	repo := data.NewCatalogRepo(d.DB)
	ctx := context.Background()

	// Empty name → no category.
	if id, err := repo.EnsureCategoryUnder(ctx, "  ", ""); err != nil || id != "" {
		t.Fatalf("blank name should yield empty id: %q %v", id, err)
	}

	// Create a top-level department, then a sub-category under it.
	dept, err := repo.EnsureCategoryUnder(ctx, "Electronics", "")
	if err != nil || dept == "" {
		t.Fatalf("ensure department: %q %v", dept, err)
	}
	phones, err := repo.EnsureCategoryUnder(ctx, "Phones", dept)
	if err != nil || phones == "" {
		t.Fatalf("ensure sub-category: %q %v", phones, err)
	}

	// Idempotent: same names resolve to the same ids (case-insensitive).
	if got, _ := repo.EnsureCategoryUnder(ctx, "electronics", ""); got != dept {
		t.Errorf("department not idempotent: %q vs %q", got, dept)
	}
	if got, _ := repo.EnsureCategoryUnder(ctx, "phones", dept); got != phones {
		t.Errorf("sub-category not idempotent: %q vs %q", got, phones)
	}

	// Parent-scoped: "Phones" under a different department is a distinct row.
	grocery, _ := repo.EnsureCategoryUnder(ctx, "Grocery", "")
	phonesG, _ := repo.EnsureCategoryUnder(ctx, "Phones", grocery)
	if phonesG == phones {
		t.Error("same category name under a different department must be distinct")
	}

	// The department was created top-level (no parent).
	var parent any
	if err := d.DB.QueryRowContext(ctx, `SELECT parent_id FROM categories WHERE id = ?`, dept).Scan(&parent); err != nil {
		t.Fatal(err)
	}
	if parent != nil {
		t.Errorf("department must be top-level, parent_id = %v", parent)
	}
}
