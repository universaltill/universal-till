package data_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// TestCreateItem_NoSKUStoresNullNotUUID is ut-docs#1176: an item created (or
// imported) without a source SKU used to have its own internal UUID copied
// into the sku column, which then leaked verbatim into every staff-facing
// surface that displays SKU. sku is a nullable UNIQUE column (001_init.sql),
// so storing NULL for "no real SKU" is enough — and, unlike a UUID, a NULL
// never shows up where a shop operator can see it.
func TestCreateItem_NoSKUStoresNullNotUUID(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	id, err := repo.CreateItem(ctx, catalogtypes.ItemInput{
		Name: "Mystery Item", BasePrice: 100, IsActive: true,
	})
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}

	var sku sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT sku FROM items WHERE id = ?`, id).Scan(&sku); err != nil {
		t.Fatalf("query sku: %v", err)
	}
	if sku.Valid {
		t.Fatalf("expected sku to be NULL for an item with no source SKU, got %q (id=%q) — the UUID must not land in sku", sku.String, id)
	}
}

// TestCreateItemTx_NoSKUStoresNullNotUUID mirrors the above for the
// transactional path the .bkp/CSV importer uses.
func TestCreateItemTx_NoSKUStoresNullNotUUID(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	id, err := repo.CreateItemTx(ctx, tx, catalogtypes.ItemInput{
		Name: "Imported No-SKU Item", BasePrice: 250, IsActive: true,
	})
	if err != nil {
		t.Fatalf("CreateItemTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	var sku sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT sku FROM items WHERE id = ?`, id).Scan(&sku); err != nil {
		t.Fatalf("query sku: %v", err)
	}
	if sku.Valid {
		t.Fatalf("expected sku to be NULL for an item with no source SKU, got %q (id=%q) — the UUID must not land in sku", sku.String, id)
	}
}

// TestCreateItem_TwoItemsWithNoSKUDoNotCollide is the reason the old code
// used the item's own UUID as a sku fallback in the first place: sku is
// UNIQUE, and two rows both storing the empty string would collide on the
// second insert. Storing NULL avoids that — SQLite treats NULLs as distinct
// from each other under a UNIQUE constraint — without needing a display
// value at all.
func TestCreateItem_TwoItemsWithNoSKUDoNotCollide(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	if _, err := repo.CreateItem(ctx, catalogtypes.ItemInput{Name: "First No-SKU", BasePrice: 100, IsActive: true}); err != nil {
		t.Fatalf("CreateItem (first): %v", err)
	}
	if _, err := repo.CreateItem(ctx, catalogtypes.ItemInput{Name: "Second No-SKU", BasePrice: 200, IsActive: true}); err != nil {
		t.Fatalf("CreateItem (second) must not collide on sku uniqueness: %v", err)
	}
}

// TestListItems_NoSKUReturnsEmptyStringNotUUID is ut-docs#1176's acceptance
// criterion for the Inventory/Catalog listing surfaces: ListItems must
// tolerate the now-nullable sku column (a bare, non-COALESCEd scan would
// error on every no-SKU row) and must report "" rather than any UUID.
func TestListItems_NoSKUReturnsEmptyStringNotUUID(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	id, err := repo.CreateItem(ctx, catalogtypes.ItemInput{
		Name: "No SKU Listed", BasePrice: 300, IsActive: true,
	})
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}

	items, err := repo.ListItems(ctx)
	if err != nil {
		t.Fatalf("ListItems must tolerate a NULL sku column, got: %v", err)
	}
	var found bool
	for _, it := range items {
		if it.ID != id {
			continue
		}
		found = true
		if it.SKU != "" {
			t.Fatalf("expected empty SKU for an item with no real SKU, got %q", it.SKU)
		}
	}
	if !found {
		t.Fatalf("expected to find item %q in ListItems", id)
	}
}
