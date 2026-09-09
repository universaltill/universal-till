package data_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
)

// ut-docs#1900: the manual add-variant row's SKU field has always promised
// "auto if blank" (catalog.auto_if_blank), but CreateVariant stored a blank
// SKU as SQL NULL and generated nothing. The option-set generator relies on
// every generated variant carrying a real SKU, so the fix lands here, on the
// shared insert path, and covers the manual row too.
func TestCreateVariant_BlankSKUGetsGeneratedSKU(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "autosku.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	ctx := context.Background()
	repo := data.NewCatalogRepo(d.DB)

	itemID, err := repo.CreateItem(ctx, catalogtypes.ItemInput{Name: "Coffee", BasePrice: 300, IsActive: true})
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}
	v1, err := repo.CreateVariant(ctx, catalogtypes.VariantInput{ItemID: itemID, SKU: "", Name: "Large", Price: 350, IsActive: true})
	if err != nil {
		t.Fatalf("CreateVariant: %v", err)
	}
	v2, err := repo.CreateVariant(ctx, catalogtypes.VariantInput{ItemID: itemID, SKU: "   ", Name: "Small", Price: 300, IsActive: true})
	if err != nil {
		t.Fatalf("CreateVariant (whitespace sku): %v", err)
	}

	var sku1, sku2 sql.NullString
	if err := d.DB.QueryRowContext(ctx, `SELECT sku FROM item_variants WHERE id = ?`, v1).Scan(&sku1); err != nil {
		t.Fatal(err)
	}
	if err := d.DB.QueryRowContext(ctx, `SELECT sku FROM item_variants WHERE id = ?`, v2).Scan(&sku2); err != nil {
		t.Fatal(err)
	}
	if !sku1.Valid || sku1.String == "" {
		t.Fatalf("blank SKU must be auto-generated, persisted sku = %+v (NULL/blank)", sku1)
	}
	if !sku2.Valid || sku2.String == "" {
		t.Fatalf("whitespace SKU must be auto-generated, persisted sku = %+v (NULL/blank)", sku2)
	}
	if sku1.String == sku2.String {
		t.Fatalf("two auto-generated SKUs collided: %q", sku1.String)
	}

	// An explicit SKU is still stored verbatim — the generator only fills a
	// blank, it never overrides what the operator typed.
	v3, err := repo.CreateVariant(ctx, catalogtypes.VariantInput{ItemID: itemID, SKU: "COF-XL", Name: "XL", Price: 400, IsActive: true})
	if err != nil {
		t.Fatal(err)
	}
	var sku3 string
	if err := d.DB.QueryRowContext(ctx, `SELECT sku FROM item_variants WHERE id = ?`, v3).Scan(&sku3); err != nil {
		t.Fatal(err)
	}
	if sku3 != "COF-XL" {
		t.Fatalf("explicit SKU must be kept verbatim, got %q", sku3)
	}
}
