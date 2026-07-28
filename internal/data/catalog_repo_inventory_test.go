package data_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// The inventory-row creation added to CreateItem must be genuinely
// best-effort: several existing test suites (and, in principle, a real
// stockless deployment) use a schema with no stock_locations table at all.
// Item creation itself must still succeed. This is a real regression test —
// the first version of this fix returned the inventory error from
// CreateItem, which broke TestCatalogCreateAndDeactivate and others by
// turning every item creation into a 400 wherever stock_locations didn't
// exist.
func TestCreateItem_SucceedsWithoutStockLocationsTable(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()

	repo := data.NewCatalogRepo(db)
	id, err := repo.CreateItem(context.Background(), catalogtypes.ItemInput{
		Name: "Latte", BasePrice: 320, IsActive: true,
	})
	if err != nil {
		t.Fatalf("CreateItem must succeed even when inventory tracking is unavailable: %v", err)
	}
	if id == "" {
		t.Fatal("expected a non-empty item id")
	}
}

// A newly created item/variant had NO row in inventory at all until some
// other stock action happened to touch it — invisible on the Inventory page
// (ListStockLevels starts from an INNER JOIN on inventory) despite showing
// up fine in the catalog list. Confirmed live 2026-07-29: an item added
// through the catalog form was "only in catalog," missing from Inventory.
func TestCreateItem_CreatesInventoryRow(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "cat.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	ctx := context.Background()
	repo := data.NewCatalogRepo(d.DB)

	id, err := repo.CreateItem(ctx, catalogtypes.ItemInput{
		Name: "Latte", BasePrice: 320, IsActive: true,
	})
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}

	var qty float64
	var locationID string
	err = d.DB.QueryRowContext(ctx,
		`SELECT quantity, location_id FROM inventory WHERE item_id = ?`, id,
	).Scan(&qty, &locationID)
	if err != nil {
		t.Fatalf("expected an inventory row for the new item, got: %v", err)
	}
	if qty != 0 {
		t.Fatalf("expected initial quantity 0, got %v", qty)
	}
	if locationID == "" {
		t.Fatal("expected a default location to be assigned")
	}

	// A second item must land at the SAME default location, not create a
	// duplicate "Main" location each time.
	id2, err := repo.CreateItem(ctx, catalogtypes.ItemInput{
		Name: "Cappuccino", BasePrice: 320, IsActive: true,
	})
	if err != nil {
		t.Fatalf("CreateItem #2: %v", err)
	}
	var locationID2 string
	if err := d.DB.QueryRowContext(ctx,
		`SELECT location_id FROM inventory WHERE item_id = ?`, id2,
	).Scan(&locationID2); err != nil {
		t.Fatalf("expected an inventory row for the second item: %v", err)
	}
	if locationID2 != locationID {
		t.Fatalf("expected the same default location, got %q then %q", locationID, locationID2)
	}
}

func TestCreateVariant_CreatesInventoryRow(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "cat.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	ctx := context.Background()
	repo := data.NewCatalogRepo(d.DB)

	itemID, err := repo.CreateItem(ctx, catalogtypes.ItemInput{
		Name: "Coffee", BasePrice: 300, IsActive: true,
	})
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}
	variantID, err := repo.CreateVariant(ctx, catalogtypes.VariantInput{
		ItemID: itemID, Name: "Large", Price: 350, IsActive: true,
	})
	if err != nil {
		t.Fatalf("CreateVariant: %v", err)
	}

	var qty float64
	if err := d.DB.QueryRowContext(ctx,
		`SELECT quantity FROM inventory WHERE variant_id = ?`, variantID,
	).Scan(&qty); err != nil {
		t.Fatalf("expected an inventory row for the new variant, got: %v", err)
	}
	if qty != 0 {
		t.Fatalf("expected initial quantity 0, got %v", qty)
	}
}
