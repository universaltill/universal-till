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

// CreateItem, UpdateItemReturningWasActive, CreateVariant and UpdateVariant
// are the admin catalogue form's entry points: they refuse a name holding
// control or invisible characters (data.ErrNameInvalidCharacters,
// ut-docs#5858) and store it trimmed. data.CreateItem checks too
// (ut-docs#5871); this wrapper's check is kept so the admin error precedes
// the repo's.
func CreateItem(ctx context.Context, db *sql.DB, in ItemInput) (string, error) {
	name, err := data.CheckSingleLineName(in.Name)
	if err != nil {
		return "", err
	}
	in.Name = name
	return data.NewCatalogRepo(db).CreateItem(ctx, catalogtypes.ItemInput(in))
}

// UpdateItemReturningWasActive is the catalog item update plus the item's
// previous is_active state, read and written atomically (ut-docs#1399) so a
// caller deciding an OOB re-render mode from that state can't race a
// concurrent update on the same item. It replaced this file's plain
// `UpdateItem` wrapper as the catalog form's only item-update path; that
// wrapper was removed by the ut-docs#1566 dead-code burn-down once its last
// caller (a test) was pointed here.
func UpdateItemReturningWasActive(ctx context.Context, db *sql.DB, in ItemInput) (bool, error) {
	name, err := data.CheckSingleLineName(in.Name)
	if err != nil {
		return false, err
	}
	in.Name = name
	return data.NewCatalogRepo(db).UpdateItemReturningWasActive(ctx, catalogtypes.ItemInput(in))
}

func CreateVariant(ctx context.Context, db *sql.DB, in VariantInput) (string, error) {
	name, err := data.CheckSingleLineName(in.Name)
	if err != nil {
		return "", err
	}
	in.Name = name
	return data.NewCatalogRepo(db).CreateVariant(ctx, catalogtypes.VariantInput(in))
}

func UpdateVariant(ctx context.Context, db *sql.DB, in VariantInput) error {
	name, err := data.CheckSingleLineName(in.Name)
	if err != nil {
		return err
	}
	in.Name = name
	return data.NewCatalogRepo(db).UpdateVariant(ctx, catalogtypes.VariantInput(in))
}

func AddBarcode(ctx context.Context, db *sql.DB, in BarcodeInput) error {
	return data.NewCatalogRepo(db).AddBarcode(ctx, catalogtypes.BarcodeInput(in))
}

// RemoveBarcode detaches a barcode from whatever it is attached to.
func RemoveBarcode(ctx context.Context, db *sql.DB, barcode string) error {
	return data.NewCatalogRepo(db).DeleteBarcode(ctx, barcode)
}
