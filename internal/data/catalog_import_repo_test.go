package data_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/catalogtypes"
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

// TestItemExistsByNameAndCategory (ut-docs#1839) is the fallback identity
// check for a codeless import row (no SKU, no barcode) — a real SumUp café
// export is exactly this shape, and until this method existed nothing
// compared such a row against the catalog at all, so re-importing the same
// file duplicated it. The check is read-only (no EnsureCategoryUnder side
// effects) and scoped by (name, department, category) so two genuinely
// distinct items sharing a name in different categories stay distinguishable.
func TestItemExistsByNameAndCategory(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "cat.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	repo := data.NewCatalogRepo(d.DB)
	ctx := context.Background()

	// Nothing imported yet — no match anywhere, department/category chain
	// doesn't exist yet either.
	if exists, err := repo.ItemExistsByNameAndCategory(ctx, "Eistee", "", "Drinks"); err != nil || exists {
		t.Fatalf("empty catalog: exists=%v err=%v, want false/nil", exists, err)
	}

	// Create the category chain and an item in it, the same way import
	// commit does: EnsureCategoryUnder(department, "") then
	// EnsureCategoryUnder(category, deptID).
	drinks, err := repo.EnsureCategoryUnder(ctx, "Drinks", "")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := d.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateItemTx(ctx, tx, catalogtypes.ItemInput{
		Name: "Eistee", BasePrice: 250, CategoryID: &drinks, IsActive: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	// Same name + same category (case/whitespace-insensitive, matching
	// EnsureCategoryUnder's own COLLATE NOCASE convention) → duplicate.
	if exists, err := repo.ItemExistsByNameAndCategory(ctx, " eistee ", "", "Drinks"); err != nil || !exists {
		t.Fatalf("same name/category: exists=%v err=%v, want true/nil", exists, err)
	}

	// Same name, no category at all this time → the item is filed under
	// "Drinks", a codeless row with NO category is not the same identity.
	if exists, err := repo.ItemExistsByNameAndCategory(ctx, "Eistee", "", ""); err != nil || exists {
		t.Fatalf("same name, no category: exists=%v err=%v, want false/nil", exists, err)
	}

	// Same name, a DIFFERENT (not-yet-existing) category → not a duplicate;
	// this is the "two distinct items sharing a name" case the card asks
	// not to block.
	if exists, err := repo.ItemExistsByNameAndCategory(ctx, "Eistee", "", "Snacks"); err != nil || exists {
		t.Fatalf("same name, different category: exists=%v err=%v, want false/nil", exists, err)
	}

	// Same check again, but with "Snacks" now a REAL, already-existing
	// top-level category (not merely absent) -- the assertion above alone
	// passes for the weaker reason that "Snacks" didn't exist yet and
	// findCategoryIDUnder short-circuits to false. This is the actual
	// "two distinct products share a name in different categories" case:
	// both categories genuinely exist, and the query must still not match.
	if _, err := repo.EnsureCategoryUnder(ctx, "Snacks", ""); err != nil {
		t.Fatal(err)
	}
	if exists, err := repo.ItemExistsByNameAndCategory(ctx, "Eistee", "", "Snacks"); err != nil || exists {
		t.Fatalf("same name, different EXISTING category: exists=%v err=%v, want false/nil", exists, err)
	}

	// Department-nested category: an item filed under Department>Category
	// must match on that same pairing, and NOT match a same-named category
	// under a different (or no) department (parent-scoped, same as
	// EnsureCategoryUnder itself).
	snacksDept, err := repo.EnsureCategoryUnder(ctx, "Food", "")
	if err != nil {
		t.Fatal(err)
	}
	snacksCat, err := repo.EnsureCategoryUnder(ctx, "Snacks", snacksDept)
	if err != nil {
		t.Fatal(err)
	}
	tx2, err := d.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateItemTx(ctx, tx2, catalogtypes.ItemInput{
		Name: "Chips", BasePrice: 199, CategoryID: &snacksCat, IsActive: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := tx2.Commit(); err != nil {
		t.Fatal(err)
	}
	if exists, err := repo.ItemExistsByNameAndCategory(ctx, "Chips", "Food", "Snacks"); err != nil || !exists {
		t.Fatalf("department-nested match: exists=%v err=%v, want true/nil", exists, err)
	}
	if exists, err := repo.ItemExistsByNameAndCategory(ctx, "Chips", "", "Snacks"); err != nil || exists {
		t.Fatalf("same category name with no department must not match a department-nested row: exists=%v err=%v, want false/nil", exists, err)
	}
}
