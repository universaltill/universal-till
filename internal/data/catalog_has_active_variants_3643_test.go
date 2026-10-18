package data_test

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// HasActiveVariants backs the cloud adjust_stock refusal of an item-level
// adjustment on a variant-stocked item (ADR-0043, ut-docs#3643): only an
// ACTIVE variant counts, SKU or not.
func TestHasActiveVariants(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "plain", SKU: "P1", Name: "Plain", BasePrice: 100, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "shirt", SKU: "S1", Name: "Shirt", BasePrice: 100, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v1", ItemID: "shirt", SKU: "", Name: "Large", Price: 150, IsActive: true})

	for _, tc := range []struct {
		item string
		want bool
	}{{"plain", false}, {"shirt", true}, {"missing", false}} {
		got, err := repo.HasActiveVariants(ctx, tc.item)
		if err != nil || got != tc.want {
			t.Fatalf("HasActiveVariants(%q) = %v, %v; want %v", tc.item, got, err, tc.want)
		}
	}

	if _, err := db.Exec(`UPDATE item_variants SET is_active = 0 WHERE id = 'v1'`); err != nil {
		t.Fatal(err)
	}
	if got, err := repo.HasActiveVariants(ctx, "shirt"); err != nil || got {
		t.Fatalf("only an inactive variant left: got %v, %v; want false", got, err)
	}
}
