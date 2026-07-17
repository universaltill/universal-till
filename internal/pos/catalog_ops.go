package pos

import (
	"context"
	"database/sql"

	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/data"
)

// Thin wrappers to keep POS callers and tests using repository-based catalog operations without inline SQL.

func DeactivateItem(ctx context.Context, db *sql.DB, itemID string) error {
	return data.NewCatalogRepo(db).DeactivateItem(ctx, itemID)
}

func DeactivateVariant(ctx context.Context, db *sql.DB, variantID string) error {
	return data.NewCatalogRepo(db).DeactivateVariant(ctx, variantID)
}

func CreateItem(ctx context.Context, db *sql.DB, in ItemInput) (string, error) {
	return data.NewCatalogRepo(db).CreateItem(ctx, catalogtypes.ItemInput(in))
}

func UpdateItem(ctx context.Context, db *sql.DB, in ItemInput) error {
	return data.NewCatalogRepo(db).UpdateItem(ctx, catalogtypes.ItemInput(in))
}

func CreateVariant(ctx context.Context, db *sql.DB, in VariantInput) (string, error) {
	return data.NewCatalogRepo(db).CreateVariant(ctx, catalogtypes.VariantInput(in))
}

func UpdateVariant(ctx context.Context, db *sql.DB, in VariantInput) error {
	return data.NewCatalogRepo(db).UpdateVariant(ctx, catalogtypes.VariantInput(in))
}

func AddBarcode(ctx context.Context, db *sql.DB, in BarcodeInput) error {
	return data.NewCatalogRepo(db).AddBarcode(ctx, catalogtypes.BarcodeInput(in))
}

// RemoveBarcode detaches a barcode from whatever it is attached to.
func RemoveBarcode(ctx context.Context, db *sql.DB, barcode string) error {
	return data.NewCatalogRepo(db).DeleteBarcode(ctx, barcode)
}
