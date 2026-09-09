package data_test

// ut-docs#1901: an item's tile color round-trips through CreateItem/
// CreateItemTx/UpdateItem/ListItems/GetItem exactly like any other plain
// optional column (category_id, brand_id, ...) — the repo layer stores
// whatever string it's given; the fixed-palette allowlist itself is
// enforced one layer up, in internal/pages/catalog's validateLookups
// (see internal/catalogtypes's own TestValidItemColor_* for that
// allowlist's unit tests, and handlers_coverage_test.go's
// TestItemCreate_InputValidation for the end-to-end "invalid color is
// rejected with 400" case).

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/testsupport"
)

func TestCreateItem_WithColor_RoundTrips(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	id, err := repo.CreateItem(ctx, catalogtypes.ItemInput{
		Name: "Latte", BasePrice: 320, IsActive: true, Color: "#0f172a",
	})
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}

	items, err := repo.ListItems(ctx)
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	if len(items) != 1 || items[0].Color != "#0f172a" {
		t.Fatalf("expected the created item's color to round-trip via ListItems, got %+v", items)
	}

	got, ok, err := repo.GetItem(ctx, id)
	if err != nil || !ok {
		t.Fatalf("GetItem: ok=%v err=%v", ok, err)
	}
	if got.Color != "#0f172a" {
		t.Fatalf("expected GetItem to return the color, got %q", got.Color)
	}
}

// An item created with no color at all must read back as "" — never a
// literal "null" or similar NULL-scan artifact — same convention as
// CategoryID/BrandID being nil rather than a sentinel string.
func TestCreateItem_WithoutColor_ReadsBackEmpty(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	id, err := repo.CreateItem(ctx, catalogtypes.ItemInput{Name: "Plain Item", BasePrice: 100, IsActive: true})
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}
	got, ok, err := repo.GetItem(ctx, id)
	if err != nil || !ok {
		t.Fatalf("GetItem: ok=%v err=%v", ok, err)
	}
	if got.Color != "" {
		t.Fatalf("expected no color, got %q", got.Color)
	}
}

func TestCreateItemTx_WithColor_RoundTrips(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	id, err := repo.CreateItemTx(ctx, tx, catalogtypes.ItemInput{
		Name: "Croissant", BasePrice: 250, IsActive: true, Color: "#7c3aed",
	})
	if err != nil {
		t.Fatalf("CreateItemTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	got, ok, err := repo.GetItem(ctx, id)
	if err != nil || !ok {
		t.Fatalf("GetItem: ok=%v err=%v", ok, err)
	}
	if got.Color != "#7c3aed" {
		t.Fatalf("expected the color set via CreateItemTx to round-trip, got %q", got.Color)
	}
}

// UpdateItem must both SET a color on an item that had none, and CLEAR one
// back to "" when the update submits an empty value (the "no color" swatch
// tile in the picker) — unlike SKU's COALESCE(NULLIF) preserve-on-blank
// behavior, color follows the same plain-overwrite convention as
// category_id/brand_id/description.
func TestUpdateItem_SetsAndClearsColor(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Item", BasePrice: 100, IsActive: true})

	if err := repo.UpdateItem(ctx, catalogtypes.ItemInput{
		ID: "i1", Name: "Item", BasePrice: 100, IsActive: true, Color: "#be185d",
	}); err != nil {
		t.Fatalf("UpdateItem (set color): %v", err)
	}
	got, ok, err := repo.GetItem(ctx, "i1")
	if err != nil || !ok {
		t.Fatalf("GetItem: ok=%v err=%v", ok, err)
	}
	if got.Color != "#be185d" {
		t.Fatalf("expected color to be set, got %q", got.Color)
	}

	// Re-save with Color left blank (the "no color" tile) — must clear it.
	if err := repo.UpdateItem(ctx, catalogtypes.ItemInput{
		ID: "i1", Name: "Item", BasePrice: 100, IsActive: true, Color: "",
	}); err != nil {
		t.Fatalf("UpdateItem (clear color): %v", err)
	}
	got, ok, err = repo.GetItem(ctx, "i1")
	if err != nil || !ok {
		t.Fatalf("GetItem: ok=%v err=%v", ok, err)
	}
	if got.Color != "" {
		t.Fatalf("expected color to be cleared, got %q", got.Color)
	}
}
