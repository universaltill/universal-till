package data_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// TestCreateItem_NoSKUGetsReadableSKUNotUUID is ut-docs#1176 as amended by
// ut-docs#3087: an item created (or imported) without a source SKU used to
// have its own internal UUID copied into the sku column, which then leaked
// verbatim into every staff-facing surface that displays SKU. #1176 stored
// NULL instead; the product owner's rule since #3087 is that no item exists
// without a SKU, so it now gets a generated, readable one — still never
// the UUID.
func TestCreateItem_NoSKUGetsReadableSKUNotUUID(t *testing.T) {
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
	if !sku.Valid || sku.String == "" {
		t.Fatalf("an item with no source SKU must get a generated one (ut-docs#3087), got %+v", sku)
	}
	if strings.Contains(id, sku.String) || strings.Contains(sku.String, id[:8]) {
		t.Fatalf("generated sku %q is derived from the item's UUID %q — ut-docs#1176", sku.String, id)
	}
}

// TestCreateItemTx_NoSKUGetsReadableSKUNotUUID mirrors the above for the
// transactional path the .bkp/CSV importer uses.
func TestCreateItemTx_NoSKUGetsReadableSKUNotUUID(t *testing.T) {
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
	if !sku.Valid || sku.String == "" {
		t.Fatalf("an item with no source SKU must get a generated one (ut-docs#3087), got %+v", sku)
	}
	if strings.Contains(id, sku.String) || strings.Contains(sku.String, id[:8]) {
		t.Fatalf("generated sku %q is derived from the item's UUID %q — ut-docs#1176", sku.String, id)
	}
}

// TestCreateItem_TwoItemsWithNoSKUDoNotCollide is the reason the old code
// used the item's own UUID as a sku fallback in the first place: sku is
// UNIQUE, and two rows both storing the same value would collide on the
// second insert. The generator hands each a different SKU (ut-docs#3087).
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
// tolerate a NULL sku column (a bare, non-COALESCEd scan would error on
// every no-SKU row) and must report "" rather than any UUID. New items
// always get a SKU since ut-docs#3087, but tills upgraded from before it
// still hold NULL-SKU rows until the backfill (ut-docs#3097) runs, so the
// row is forced to NULL here.
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
	if _, err := db.ExecContext(ctx, `UPDATE items SET sku = NULL WHERE id = ?`, id); err != nil {
		t.Fatalf("force legacy NULL sku: %v", err)
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
