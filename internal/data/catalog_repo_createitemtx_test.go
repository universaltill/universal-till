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

// TestCreateItemTx_CommitsItemAndInventoryRow confirms CreateItemTx behaves
// like CreateItem once the caller commits: the item lands with its
// zero-quantity inventory row (ut-docs#310).
func TestCreateItemTx_CommitsItemAndInventoryRow(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "cat.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	ctx := context.Background()
	repo := data.NewCatalogRepo(d.DB)

	tx, err := d.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	id, err := repo.CreateItemTx(ctx, tx, catalogtypes.ItemInput{
		Name: "Latte", BasePrice: 320, IsActive: true,
	})
	if err != nil {
		t.Fatalf("CreateItemTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	var qty float64
	if err := d.DB.QueryRowContext(ctx,
		`SELECT quantity FROM inventory WHERE item_id = ?`, id,
	).Scan(&qty); err != nil {
		t.Fatalf("expected an inventory row for the new item, got: %v", err)
	}
	if qty != 0 {
		t.Fatalf("expected initial quantity 0, got %v", qty)
	}
}

// TestCreateItemTx_RollbackDiscardsItem is the point of threading a *sql.Tx
// through CreateItemTx at all: if the caller rolls back (because a later
// step in the same row failed unexpectedly), the item must not have landed
// either — no half-built row survives the rollback.
func TestCreateItemTx_RollbackDiscardsItem(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "cat.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	ctx := context.Background()
	repo := data.NewCatalogRepo(d.DB)

	tx, err := d.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	id, err := repo.CreateItemTx(ctx, tx, catalogtypes.ItemInput{
		ID: "rollback-item", SKU: "ROLLBACK1", Name: "Discarded", BasePrice: 100, IsActive: true,
	})
	if err != nil {
		t.Fatalf("CreateItemTx: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	var n int
	if err := d.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM items WHERE id = ?`, id,
	).Scan(&n); err != nil {
		t.Fatalf("count items: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected the rolled-back item to be gone, found %d row(s)", n)
	}
}

// TestCreateItemTx_SucceedsWithoutStockLocationsTable mirrors
// TestCreateItem_SucceedsWithoutStockLocationsTable: CreateItemTx's
// inventory-row step must stay exactly as best-effort as CreateItem's own —
// a stockless deployment (no stock_locations table) must still be able to
// commit an item through the transactional path, same as the non-tx one.
func TestCreateItemTx_SucceedsWithoutStockLocationsTable(t *testing.T) {
	sqlDB := testsupport.NewCatalogTestDB(t)
	defer sqlDB.Close()
	repo := data.NewCatalogRepo(sqlDB)
	ctx := context.Background()

	tx, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	id, err := repo.CreateItemTx(ctx, tx, catalogtypes.ItemInput{
		Name: "Latte", BasePrice: 320, IsActive: true,
	})
	if err != nil {
		t.Fatalf("CreateItemTx must succeed even when inventory tracking is unavailable: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if id == "" {
		t.Fatal("expected a non-empty item id")
	}
}
