package data

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/testsupport"
)

// History: tea+milk co-occur in 3 baskets, tea+sugar in 2, tea+crisps only
// once (below min support), and bread never sells with tea.
func TestRelatedItems_RebuildAndSuggest(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "tea", SKU: "TEA", Name: "Tea", BasePrice: 250, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "milk", SKU: "MILK", Name: "Milk", BasePrice: 130, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "sugar", SKU: "SUGAR", Name: "Sugar", BasePrice: 90, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "crisps", SKU: "CRISPS", Name: "Crisps", BasePrice: 110, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "bread", SKU: "BREAD", Name: "Bread", BasePrice: 140, IsActive: true})
	testsupport.SeedImage(t, db, "img-milk", "milk", "/public/assets/items/milk/thumb.png")

	// 3× tea+milk, 2× of those also sugar, 1× tea+crisps, bread alone.
	testsupport.SeedCompletedSale(t, db, "s1", "tea", "milk", "sugar")
	testsupport.SeedCompletedSale(t, db, "s2", "tea", "milk", "sugar")
	testsupport.SeedCompletedSale(t, db, "s3", "tea", "milk")
	testsupport.SeedCompletedSale(t, db, "s4", "tea", "crisps")
	testsupport.SeedCompletedSale(t, db, "s5", "bread")

	repo := NewRelatedItemsRepo(db)
	ctx := context.Background()

	n, err := repo.Rebuild(ctx)
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if n == 0 {
		t.Fatalf("expected co-occurrence rows, got none")
	}

	// Basket contains tea → milk should be the top suggestion, sugar second;
	// crisps (support 1 < min 2) and bread (never together) must not appear.
	got, err := repo.SuggestForBasket(ctx, []string{"tea"}, 4)
	if err != nil {
		t.Fatalf("suggest: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 suggestions, got %d: %+v", len(got), got)
	}
	if got[0].ItemID != "milk" || got[1].ItemID != "sugar" {
		t.Fatalf("unexpected ranking: %+v", got)
	}
	if got[0].SKU != "MILK" || got[0].PriceMinor != 130 {
		t.Fatalf("suggestion missing display fields: %+v", got[0])
	}
	if got[0].ImageURL != "/public/assets/items/milk/thumb.png" {
		t.Fatalf("expected thumbnail path, got %q", got[0].ImageURL)
	}

	// Items already in the basket are excluded.
	got, err = repo.SuggestForBasket(ctx, []string{"tea", "milk"}, 4)
	if err != nil {
		t.Fatalf("suggest with milk in basket: %v", err)
	}
	for _, s := range got {
		if s.ItemID == "milk" || s.ItemID == "tea" {
			t.Fatalf("suggested an item already in the basket: %+v", s)
		}
	}

	// Empty basket → no suggestions, no error.
	got, err = repo.SuggestForBasket(ctx, nil, 4)
	if err != nil || got != nil {
		t.Fatalf("empty basket should be silent, got %v %v", got, err)
	}
}

func TestRelatedItems_InactiveAndSkulessExcluded(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "tea", SKU: "TEA", Name: "Tea", BasePrice: 250, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "gone", SKU: "GONE", Name: "Delisted", BasePrice: 100, IsActive: false})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "nosku", SKU: "", Name: "No SKU", BasePrice: 100, IsActive: true})

	testsupport.SeedCompletedSale(t, db, "s1", "tea", "gone", "nosku")
	testsupport.SeedCompletedSale(t, db, "s2", "tea", "gone", "nosku")

	repo := NewRelatedItemsRepo(db)
	ctx := context.Background()
	if _, err := repo.Rebuild(ctx); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	got, err := repo.SuggestForBasket(ctx, []string{"tea"}, 4)
	if err != nil {
		t.Fatalf("suggest: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("inactive/SKU-less items must not be suggested: %+v", got)
	}
}

// A variant sale line (item_id NULL, variant_id set — the shape a variant
// checkout persists per 001_init.sql's CHECK constraint) must resolve
// through its parent item, the same COALESCE(sl.item_id, iv.item_id)-via-
// item_variants join every other reporting query already uses
// (ut-docs#752 — this query was the one exception).
func TestRelatedItems_VariantSaleLineContributes(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "shirt", SKU: "SHIRT", Name: "Shirt", BasePrice: 2000, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "shirt-m", ItemID: "shirt", SKU: "SHIRT-M", Name: "Shirt (M)", Price: 2000, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "belt", SKU: "BELT", Name: "Belt", BasePrice: 1500, IsActive: true})

	// Two baskets pairing the shirt VARIANT (not the bare item) with belt —
	// meets relatedMinSupport (2) only if the variant line resolves to
	// "shirt" at all; before the fix this query never saw these lines.
	testsupport.SeedCompletedSaleVariant(t, db, "s1", "shirt-m")
	if _, err := db.Exec(`INSERT INTO sale_lines(id, sale_id, line_no, item_id, name_snapshot) VALUES(?,?,?,?,?)`,
		"s1-lb", "s1", 2, "belt", "belt"); err != nil {
		t.Fatalf("seed belt line: %v", err)
	}
	testsupport.SeedCompletedSaleVariant(t, db, "s2", "shirt-m")
	if _, err := db.Exec(`INSERT INTO sale_lines(id, sale_id, line_no, item_id, name_snapshot) VALUES(?,?,?,?,?)`,
		"s2-lb", "s2", 2, "belt", "belt"); err != nil {
		t.Fatalf("seed belt line: %v", err)
	}

	repo := NewRelatedItemsRepo(db)
	ctx := context.Background()
	if _, err := repo.Rebuild(ctx); err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	got, err := repo.SuggestForBasket(ctx, []string{"shirt"}, 4)
	if err != nil {
		t.Fatalf("suggest: %v", err)
	}
	if len(got) != 1 || got[0].ItemID != "belt" {
		t.Fatalf("expected belt suggested via the variant-line purchases, got %+v", got)
	}
}

func TestRelatedItems_RebuildReplacesOldPairs(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "a", SKU: "A", Name: "A", BasePrice: 100, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "b", SKU: "B", Name: "B", BasePrice: 100, IsActive: true})

	// A stale pair that the current history no longer supports.
	if _, err := db.Exec(`INSERT INTO related_items(item_id, related_item_id, support, score) VALUES('a','b',9,9.0)`); err != nil {
		t.Fatalf("seed stale pair: %v", err)
	}

	repo := NewRelatedItemsRepo(db)
	ctx := context.Background()
	if _, err := repo.Rebuild(ctx); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	got, err := repo.SuggestForBasket(ctx, []string{"a"}, 4)
	if err != nil {
		t.Fatalf("suggest: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("rebuild must clear stale pairs: %+v", got)
	}
}
