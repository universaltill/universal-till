package data_test

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// TestCreateItem_StockUntracked_DefaultsFalse verifies that CreateItem
// doesn't need to know about stock_untracked to leave an item tracked
// (ut-docs#1850) — the DB column's own DEFAULT 0 plus the field's false
// Go zero value both mean "tracked", so an existing call site that never
// sets StockUntracked is unaffected.
func TestCreateItem_StockUntracked_DefaultsFalse(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	id, err := repo.CreateItem(ctx, catalogtypes.ItemInput{Name: "Widget", BasePrice: 100})
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}

	got, ok, err := repo.GetItem(ctx, id)
	if err != nil || !ok {
		t.Fatalf("GetItem: got=%v ok=%v err=%v", got, ok, err)
	}
	if got.StockUntracked {
		t.Fatalf("expected StockUntracked=false by default, got true")
	}
}

// TestCreateItem_StockUntracked_Persists verifies CreateItem/GetItem round
// trip a StockUntracked=true item correctly.
func TestCreateItem_StockUntracked_Persists(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	id, err := repo.CreateItem(ctx, catalogtypes.ItemInput{Name: "Delivery-only line", BasePrice: 500, StockUntracked: true})
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}

	got, ok, err := repo.GetItem(ctx, id)
	if err != nil || !ok {
		t.Fatalf("GetItem: got=%v ok=%v err=%v", got, ok, err)
	}
	if !got.StockUntracked {
		t.Fatalf("expected StockUntracked=true to persist, got false")
	}
}

// TestUpdateItem_StockUntracked_Toggles verifies UpdateItem can flip an
// item's stock_untracked flag in either direction.
func TestUpdateItem_StockUntracked_Toggles(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	id, err := repo.CreateItem(ctx, catalogtypes.ItemInput{Name: "Item", BasePrice: 100})
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}

	in, ok, err := repo.GetItem(ctx, id)
	if err != nil || !ok {
		t.Fatalf("GetItem: got=%v ok=%v err=%v", in, ok, err)
	}
	in.StockUntracked = true
	if err := repo.UpdateItem(ctx, in); err != nil {
		t.Fatalf("UpdateItem: %v", err)
	}

	got, ok, err := repo.GetItem(ctx, id)
	if err != nil || !ok {
		t.Fatalf("GetItem after update: got=%v ok=%v err=%v", got, ok, err)
	}
	if !got.StockUntracked {
		t.Fatalf("expected StockUntracked=true after update, got false")
	}

	got.StockUntracked = false
	if err := repo.UpdateItem(ctx, got); err != nil {
		t.Fatalf("UpdateItem (revert): %v", err)
	}
	reverted, ok, err := repo.GetItem(ctx, id)
	if err != nil || !ok {
		t.Fatalf("GetItem after revert: got=%v ok=%v err=%v", reverted, ok, err)
	}
	if reverted.StockUntracked {
		t.Fatalf("expected StockUntracked=false after revert, got true")
	}
}

// TestListItems_StockUntracked_RoundTrips ensures ListItems also carries
// the flag through — a listing that silently dropped it would make every
// downstream reader of ListItems (not just GetItem) blind to the flag.
func TestListItems_StockUntracked_RoundTrips(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	if _, err := repo.CreateItem(ctx, catalogtypes.ItemInput{Name: "Tracked", BasePrice: 100, IsActive: true}); err != nil {
		t.Fatalf("CreateItem: %v", err)
	}
	if _, err := repo.CreateItem(ctx, catalogtypes.ItemInput{Name: "Untracked", BasePrice: 100, IsActive: true, StockUntracked: true}); err != nil {
		t.Fatalf("CreateItem: %v", err)
	}

	items, err := repo.ListItems(ctx)
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	byName := map[string]bool{}
	for _, it := range items {
		byName[it.Name] = it.StockUntracked
	}
	if byName["Tracked"] {
		t.Fatalf("expected Tracked item StockUntracked=false, got true")
	}
	if !byName["Untracked"] {
		t.Fatalf("expected Untracked item StockUntracked=true, got false")
	}
}
