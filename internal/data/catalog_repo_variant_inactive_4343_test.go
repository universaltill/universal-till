package data_test

import (
	"context"
	"errors"
	"testing"

	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// ut-docs#4343: attaching a barcode to a deactivated variant is refused with
// the typed ErrVariantInactive, so callers can show a specific message
// instead of the generic "barcode attach failed".
func TestAddBarcode_InactiveVariantReturnsErrVariantInactive(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	seedSettingsTable(t, db)
	defer db.Close()
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "i1", SKU: "S1", Name: "Shirt", BasePrice: 599, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v-off", ItemID: "i1", SKU: "S1-L", Name: "L", Price: 599, IsActive: false})

	err := repo.AddBarcode(ctx, catalogtypes.BarcodeInput{Barcode: "5000004343", VariantID: "v-off"})
	if err == nil {
		t.Fatal("expected an error attaching a barcode to an inactive variant")
	}
	if !errors.Is(err, data.ErrVariantInactive) {
		t.Fatalf("errors.Is(err, ErrVariantInactive) = false; err = %v", err)
	}
}
