package data_test

// ut-docs#1356: data-layer tests for the "backfill barcodes from SKU" bulk
// admin action. ItemsWithoutBarcode finds the candidate items (active, has
// a SKU, no item_barcodes row); BarcodeOwner is the read-only dry-run
// lookup the preview endpoint uses to flag a derived code that's already
// taken, without touching ensureBarcodeAvailable's transactional path
// (that one stays load-bearing for the real write, see AddBarcode).

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/testsupport"
)

func TestItemsWithoutBarcode(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	// Eligible: active, has SKU, no barcode row at all.
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i-no-bc", SKU: "SKU-NOBC", Name: "No Barcode Item", BasePrice: 100, IsActive: true})

	// Excluded: already has a barcode.
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i-has-bc", SKU: "SKU-HASBC", Name: "Has Barcode Item", BasePrice: 100, IsActive: true})
	if _, err := db.Exec(`INSERT INTO item_barcodes(barcode, item_id, is_primary) VALUES('1112223334445','i-has-bc',1)`); err != nil {
		t.Fatal(err)
	}

	// Excluded: inactive.
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i-inactive", SKU: "SKU-INACTIVE", Name: "Inactive Item", BasePrice: 100, IsActive: false})

	// Excluded: empty SKU.
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i-no-sku", SKU: "", Name: "No SKU Item", BasePrice: 100, IsActive: true})

	got, err := repo.ItemsWithoutBarcode(ctx)
	if err != nil {
		t.Fatalf("ItemsWithoutBarcode: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 eligible item, got %d: %+v", len(got), got)
	}
	if got[0].ID != "i-no-bc" || got[0].SKU != "SKU-NOBC" || got[0].Name != "No Barcode Item" {
		t.Fatalf("unexpected eligible item: %+v", got[0])
	}
}

func TestItemsWithoutBarcodeOrderedByName(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i-z", SKU: "SKU-Z", Name: "Zebra", BasePrice: 100, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i-a", SKU: "SKU-A", Name: "Apple", BasePrice: 100, IsActive: true})

	got, err := repo.ItemsWithoutBarcode(ctx)
	if err != nil {
		t.Fatalf("ItemsWithoutBarcode: %v", err)
	}
	if len(got) != 2 || got[0].Name != "Apple" || got[1].Name != "Zebra" {
		t.Fatalf("expected [Apple, Zebra] in that order, got %+v", got)
	}
}

func TestBarcodeOwner(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Item One", BasePrice: 100, IsActive: true})
	if _, err := db.Exec(`INSERT INTO item_barcodes(barcode, item_id, is_primary) VALUES('1112223334445','i1',1)`); err != nil {
		t.Fatal(err)
	}
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v1", ItemID: "i1", SKU: "S1-V", Name: "Variant One", Price: 150, IsActive: true})
	if _, err := db.Exec(`INSERT INTO variant_barcodes(barcode, variant_id, is_primary) VALUES('9998887776665','v1',1)`); err != nil {
		t.Fatal(err)
	}

	if targetType, targetID, found, err := repo.BarcodeOwner(ctx, "1112223334445"); err != nil || !found || targetType != "item" || targetID != "i1" {
		t.Fatalf("expected item owner i1, got type=%q id=%q found=%v err=%v", targetType, targetID, found, err)
	}
	if targetType, targetID, found, err := repo.BarcodeOwner(ctx, "9998887776665"); err != nil || !found || targetType != "variant" || targetID != "v1" {
		t.Fatalf("expected variant owner v1, got type=%q id=%q found=%v err=%v", targetType, targetID, found, err)
	}
	if _, _, found, err := repo.BarcodeOwner(ctx, "0000000000000"); err != nil || found {
		t.Fatalf("expected not-found for unused code, got found=%v err=%v", found, err)
	}
}
