package data_test

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// ut-docs#1898: categories had no management screen at all — these tests
// pin the new repo methods behind that screen (create/rename/activate/
// reorder/admin-list) before the handler and template exist.

func TestCreateCategory(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	id, err := repo.CreateCategory(ctx, "Drinks")
	if err != nil || id == "" {
		t.Fatalf("expected a new category id, got %q err=%v", id, err)
	}

	cats, err := repo.ListCategories(ctx)
	if err != nil {
		t.Fatalf("ListCategories: %v", err)
	}
	if len(cats) != 1 {
		t.Fatalf("len = %d, want 1: %+v", len(cats), cats)
	}
	if cats[0].Name != "Drinks" || cats[0].ParentID != "" || !cats[0].IsActive || cats[0].SortOrder != 0 {
		t.Fatalf("unexpected first category: %+v", cats[0])
	}

	// A second category appends to the end (max sort_order + 1), never
	// interleaves and never touches parent_id (nesting is out of scope).
	id2, err := repo.CreateCategory(ctx, "Snacks")
	if err != nil {
		t.Fatalf("create second: %v", err)
	}
	cats, err = repo.ListCategories(ctx)
	if err != nil {
		t.Fatalf("ListCategories: %v", err)
	}
	if len(cats) != 2 || cats[1].ID != id2 || cats[1].SortOrder != 1 || cats[1].ParentID != "" {
		t.Fatalf("second category not appended correctly: %+v", cats)
	}

	// Blank (post-trim) name is rejected, not silently written as an empty row.
	if _, err := repo.CreateCategory(ctx, "   "); err != data.ErrCategoryNameRequired {
		t.Fatalf("blank name: err = %v, want ErrCategoryNameRequired", err)
	}
}

func TestRenameCategory(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	id, err := repo.CreateCategory(ctx, "Drinks")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.RenameCategory(ctx, id, "Beverages"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	cats, err := repo.ListCategories(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(cats) != 1 || cats[0].Name != "Beverages" || cats[0].ID != id {
		t.Fatalf("rename did not take effect, or changed the id: %+v", cats)
	}

	if err := repo.RenameCategory(ctx, id, "   "); err != data.ErrCategoryNameRequired {
		t.Fatalf("blank rename: err = %v, want ErrCategoryNameRequired", err)
	}
	if err := repo.RenameCategory(ctx, "does-not-exist", "New Name"); err != data.ErrCategoryNotFound {
		t.Fatalf("rename missing id: err = %v, want ErrCategoryNotFound", err)
	}
}

func TestSetCategoryActive(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	id, err := repo.CreateCategory(ctx, "Drinks")
	if err != nil {
		t.Fatal(err)
	}

	// No items yet — deactivation succeeds cleanly.
	if err := repo.SetCategoryActive(ctx, id, false); err != nil {
		t.Fatalf("deactivate with no items: %v", err)
	}
	cats, err := repo.ListCategories(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cats[0].IsActive {
		t.Fatalf("expected category to be inactive: %+v", cats[0])
	}

	// Reactivate.
	if err := repo.SetCategoryActive(ctx, id, true); err != nil {
		t.Fatalf("reactivate: %v", err)
	}
	cats, err = repo.ListCategories(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !cats[0].IsActive {
		t.Fatalf("expected category to be active again: %+v", cats[0])
	}

	// Now with an active item pointing at it: deactivate is BLOCKED, not a
	// silent no-op and not a cascade that also deactivates the item.
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Cola", BasePrice: 100, IsActive: true})
	if _, err := db.Exec(`UPDATE items SET category_id = ? WHERE id = 'i1'`, id); err != nil {
		t.Fatal(err)
	}
	err = repo.SetCategoryActive(ctx, id, false)
	hasItems, ok := err.(*data.ErrCategoryHasItems)
	if !ok {
		t.Fatalf("deactivate with 1 active item: err = %v, want *ErrCategoryHasItems", err)
	}
	if hasItems.Count != 1 {
		t.Fatalf("ErrCategoryHasItems.Count = %d, want 1", hasItems.Count)
	}
	cats, err = repo.ListCategories(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !cats[0].IsActive {
		t.Fatalf("blocked deactivate must not change the row: %+v", cats[0])
	}

	// An INACTIVE item pointing at the category does not count against it —
	// only active items block a deactivate.
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i2", SKU: "S2", Name: "Old Cola", BasePrice: 100, IsActive: false})
	if _, err := db.Exec(`UPDATE items SET category_id = ? WHERE id = 'i2'`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE items SET is_active = 0 WHERE id = 'i1'`); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetCategoryActive(ctx, id, false); err != nil {
		t.Fatalf("deactivate once the only referencing item is inactive: %v", err)
	}

	if err := repo.SetCategoryActive(ctx, "does-not-exist", true); err != data.ErrCategoryNotFound {
		t.Fatalf("activate missing id: err = %v, want ErrCategoryNotFound", err)
	}
}

func TestSetCategorySortOrder(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	id1, err := repo.CreateCategory(ctx, "Drinks")
	if err != nil {
		t.Fatal(err)
	}
	id2, err := repo.CreateCategory(ctx, "Snacks")
	if err != nil {
		t.Fatal(err)
	}
	id3, err := repo.CreateCategory(ctx, "Bakery")
	if err != nil {
		t.Fatal(err)
	}

	// Target order is Snacks, Bakery, Drinks — deliberately NOT alphabetical.
	// ListCategories orders by `sort_order, name`, so an implementation that
	// writes the SAME sort_order to every row (a constant instead of the loop
	// index, say) still produces name order; asserting an alphabetical target
	// would pass against that broken implementation. The per-row SortOrder
	// assertion below pins the actual integers for the same reason.
	if err := repo.SetCategorySortOrder(ctx, []string{id2, id3, id1}); err != nil {
		t.Fatalf("SetCategorySortOrder: %v", err)
	}
	cats, err := repo.ListCategories(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(cats) != 3 || cats[0].ID != id2 || cats[1].ID != id3 || cats[2].ID != id1 {
		t.Fatalf("unexpected order after reorder: %+v", cats)
	}
	for i, c := range cats {
		if c.SortOrder != i {
			t.Fatalf("cats[%d].SortOrder = %d, want %d (positions must be distinct, not collapsed): %+v", i, c.SortOrder, i, cats)
		}
	}
}

func TestListActiveCategories(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	activeID, err := repo.CreateCategory(ctx, "Drinks")
	if err != nil {
		t.Fatal(err)
	}
	inactiveID, err := repo.CreateCategory(ctx, "Retired")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SetCategoryActive(ctx, inactiveID, false); err != nil {
		t.Fatal(err)
	}

	active, err := repo.ListActiveCategories(ctx)
	if err != nil {
		t.Fatalf("ListActiveCategories: %v", err)
	}
	if len(active) != 1 || active[0].ID != activeID || !active[0].IsActive {
		t.Fatalf("ListActiveCategories must exclude the retired category: %+v", active)
	}

	// ListCategories (unfiltered) still shows both, for the admin screen's
	// reactivate path.
	all, err := repo.ListCategories(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("ListCategories must still return both rows: %+v", all)
	}
}

// TestListCategoriesForAdmin pins the item-count aggregate: zero, one and
// multiple active items, with an inactive item deliberately NOT counted
// (only active items are what SetCategoryActive's deactivate guard cares
// about, and the admin list must show the same number the guard would).
func TestListCategoriesForAdmin(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	empty, err := repo.CreateCategory(ctx, "Empty")
	if err != nil {
		t.Fatal(err)
	}
	single, err := repo.CreateCategory(ctx, "Single")
	if err != nil {
		t.Fatal(err)
	}
	multi, err := repo.CreateCategory(ctx, "Multi")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SetCategoryActive(ctx, empty, false); err != nil {
		t.Fatalf("deactivate empty category: %v", err)
	}

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "One", BasePrice: 100, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i2", SKU: "S2", Name: "Two", BasePrice: 100, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i3", SKU: "S3", Name: "Three (inactive)", BasePrice: 100, IsActive: false})
	if _, err := db.Exec(`UPDATE items SET category_id = ? WHERE id = 'i1'`, single); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE items SET category_id = ? WHERE id IN ('i2','i3')`, multi); err != nil {
		t.Fatal(err)
	}

	rows, err := repo.ListCategoriesForAdmin(ctx)
	if err != nil {
		t.Fatalf("ListCategoriesForAdmin: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("len = %d, want 3: %+v", len(rows), rows)
	}
	byID := map[string]data.CategoryAdminRow{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	if got := byID[empty]; got.ItemCount != 0 || got.IsActive {
		t.Errorf("Empty category = %+v, want ItemCount=0 IsActive=false", got)
	}
	if got := byID[single]; got.ItemCount != 1 || !got.IsActive {
		t.Errorf("Single category = %+v, want ItemCount=1 IsActive=true", got)
	}
	// multi has 2 items pointing at it but only 1 ACTIVE one — the inactive
	// item must not inflate the count.
	if got := byID[multi]; got.ItemCount != 1 || !got.IsActive {
		t.Errorf("Multi category = %+v, want ItemCount=1 (active only) IsActive=true", got)
	}
}
