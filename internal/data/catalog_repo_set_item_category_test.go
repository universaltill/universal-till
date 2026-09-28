package data_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// ut-docs#2465: CatalogRepo.SetItemCategory is the sell screen's / Designer's
// "move this quick button to another category" write -- the jiggle-mode drag
// onto a category tab and the Move to category badge's dialog both land here
// through POST /api/buttons/recategorize.
func newSetItemCategoryDB(t *testing.T) (*data.CatalogRepo, *db.DB) {
	t.Helper()
	d, err := db.Open(testsupport.MigratedDBFile(t, "set-item-category.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	for _, s := range []string{
		`INSERT INTO categories (id, name, is_active) VALUES ('cat-drinks','Drinks',1),('cat-food','Food',1),('cat-old','Retired',0)`,
		`INSERT INTO items (id, sku, name, base_price, is_active, category_id, updated_at) VALUES ('item-a','SKU-A','Latte',320,1,'cat-drinks','2000-01-01 00:00:00')`,
		`INSERT INTO items (id, sku, name, base_price, is_active, category_id) VALUES ('item-gone','SKU-G','Gone',100,0,'cat-drinks')`,
	} {
		if _, err := d.Exec(s); err != nil {
			t.Fatalf("seed %q: %v", s, err)
		}
	}
	return data.NewCatalogRepo(d.DB), d
}

func itemCategory(t *testing.T, d *db.DB, id string) sql.NullString {
	t.Helper()
	var c sql.NullString
	if err := d.QueryRow(`SELECT category_id FROM items WHERE id = ?`, id).Scan(&c); err != nil {
		t.Fatalf("read category of %s: %v", id, err)
	}
	return c
}

func TestSetItemCategory_MovesItemAndBumpsUpdatedAt(t *testing.T) {
	repo, d := newSetItemCategoryDB(t)
	ctx := context.Background()
	var genBefore, adminBefore int64
	_ = d.QueryRow(`SELECT generation FROM sell_screen_version WHERE id = 1`).Scan(&genBefore)
	_ = d.QueryRow(`SELECT generation FROM sync_admin_version WHERE id = 1`).Scan(&adminBefore)

	if err := repo.SetItemCategory(ctx, "item-a", "cat-food"); err != nil {
		t.Fatalf("SetItemCategory: %v", err)
	}
	if got := itemCategory(t, d, "item-a"); !got.Valid || got.String != "cat-food" {
		t.Fatalf("category_id = %+v, want cat-food", got)
	}
	var updated string
	if err := d.QueryRow(`SELECT updated_at FROM items WHERE id = 'item-a'`).Scan(&updated); err != nil {
		t.Fatal(err)
	}
	if updated == "2000-01-01 00:00:00" {
		t.Fatalf("updated_at was not bumped")
	}
	// The sale screen's cache/live-refresh signal is a DB trigger on items
	// (migration 047) -- this write must move it like the catalog editor's.
	var genAfter int64
	if err := d.QueryRow(`SELECT generation FROM sell_screen_version WHERE id = 1`).Scan(&genAfter); err != nil {
		t.Fatal(err)
	}
	if genAfter <= genBefore {
		t.Fatalf("sell_screen_version %d -> %d, want it to move", genBefore, genAfter)
	}
	// Other tills and the cloud pick the change up from the admin-sync
	// counter (migration 023's items trigger).
	var adminAfter int64
	if err := d.QueryRow(`SELECT generation FROM sync_admin_version WHERE id = 1`).Scan(&adminAfter); err != nil {
		t.Fatal(err)
	}
	if adminAfter <= adminBefore {
		t.Fatalf("sync_admin_version %d -> %d, want it to move", adminBefore, adminAfter)
	}
}

func TestSetItemCategory_EmptyCategoryMeansUncategorised(t *testing.T) {
	repo, d := newSetItemCategoryDB(t)
	if err := repo.SetItemCategory(context.Background(), "item-a", "  "); err != nil {
		t.Fatalf("SetItemCategory to uncategorised: %v", err)
	}
	if got := itemCategory(t, d, "item-a"); got.Valid {
		t.Fatalf("category_id = %q, want NULL (Uncategorised)", got.String)
	}
}

func TestSetItemCategory_RefusesUnknownOrInactiveCategory(t *testing.T) {
	repo, d := newSetItemCategoryDB(t)
	for _, cat := range []string{"cat-nope", "cat-old"} {
		err := repo.SetItemCategory(context.Background(), "item-a", cat)
		if !errors.Is(err, data.ErrCategoryNotFound) {
			t.Fatalf("SetItemCategory(%q) err = %v, want ErrCategoryNotFound", cat, err)
		}
		if got := itemCategory(t, d, "item-a"); got.String != "cat-drinks" {
			t.Fatalf("category changed to %q on a refused move", got.String)
		}
	}
}

func TestSetItemCategory_RefusesUnknownOrInactiveItem(t *testing.T) {
	repo, d := newSetItemCategoryDB(t)
	for _, id := range []string{"item-nope", "item-gone", ""} {
		err := repo.SetItemCategory(context.Background(), id, "cat-food")
		if !errors.Is(err, data.ErrItemNotFound) {
			t.Fatalf("SetItemCategory(item %q) err = %v, want ErrItemNotFound", id, err)
		}
	}
	if got := itemCategory(t, d, "item-gone"); got.String != "cat-drinks" {
		t.Fatalf("inactive item moved to %q", got.String)
	}
}
