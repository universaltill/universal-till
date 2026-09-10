package pos

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/money"
)

// ut-docs#1900: a variant the option-set generator created is an ordinary
// item_variants row, so it must sell through the existing CompleteSale path
// and move its own stock with NO new sale-engine code — this is the proof
// that nothing downstream needed changing. Real migrated schema (db.Open,
// same reasoning as setupVoucherDB) so the generator's tables and FKs are
// the real ones, and the sale line is shaped exactly like
// TestCompleteSale_VariantLineWithBothIDsSetIsTenderable's (both ids set,
// as a variant barcode scan produces).
func TestCompleteSale_GeneratedVariantSellsAndMovesStock(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(filepath.Join(t.TempDir(), "optset-sale.db"))
	if err != nil {
		t.Fatalf("open migrated db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	sqlDB := d.DB
	for _, s := range []string{
		`INSERT INTO stock_locations (id, name) VALUES ('loc1', 'Main')`,
		`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('tee', 'TEE', 'T-shirt', 1500, 1)`,
		`INSERT OR IGNORE INTO payment_methods (id, name, type, is_active) VALUES ('cash', 'Cash', 'cash', 1)`,
	} {
		if _, err := sqlDB.Exec(s); err != nil {
			t.Fatalf("seed %q: %v", s, err)
		}
	}

	repo := data.NewOptionSetRepo(sqlDB)
	sizeID, err := repo.CreateOptionSet(ctx, "Size")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"S", "M"} {
		if _, err := repo.AddOptionSetValue(ctx, sizeID, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.ApplyOptionSetsToItem(ctx, "tee", []string{sizeID}); err != nil {
		t.Fatal(err)
	}
	if created, err := repo.GenerateVariants(ctx, "tee"); err != nil || created != 2 {
		t.Fatalf("GenerateVariants: created=%d err=%v", created, err)
	}

	var variantID, sku string
	if err := sqlDB.QueryRow(`SELECT id, sku FROM item_variants WHERE item_id = 'tee' AND name = 'M'`).Scan(&variantID, &sku); err != nil {
		t.Fatalf("read generated variant: %v", err)
	}
	if sku == "" {
		t.Fatal("generated variant has no SKU")
	}
	// Put 5 on the shelf at loc1. CreateVariant's best-effort inventory
	// row lands at the default location, which may or may not be loc1 —
	// set the quantity on whichever row exists there, or create it.
	var invRows int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM inventory WHERE variant_id = ? AND location_id = 'loc1'`, variantID).Scan(&invRows); err != nil {
		t.Fatal(err)
	}
	if invRows == 0 {
		if _, err := sqlDB.Exec(`INSERT INTO inventory (id, item_id, variant_id, location_id, quantity, updated_at) VALUES ('inv-m', NULL, ?, 'loc1', 5, datetime('now'))`, variantID); err != nil {
			t.Fatal(err)
		}
	} else if _, err := sqlDB.Exec(`UPDATE inventory SET quantity = 5 WHERE variant_id = ? AND location_id = 'loc1'`, variantID); err != nil {
		t.Fatal(err)
	}

	// No RegisterID/CashierID: on the real schema both are FKs (registers/
	// users) and the voucher tests on this same fixture leave them unset too.
	saleID, err := CompleteSale(ctx, sqlDB, SaleInput{
		SaleType:     "sale",
		Currency:     "GBP",
		TaxInclusive: false,
		Lines: []SaleLineInput{{
			ItemID:             "tee",
			VariantID:          variantID,
			SKU:                sku,
			Name:               "T-shirt - M",
			Qty:                1,
			UnitPrice:          money.FromMinor(1500),
			TaxRateBasisPoints: 0,
			LocationID:         "loc1",
		}},
		Payments: []PaymentInput{{MethodID: "cash", Amount: money.FromMinor(1500), Currency: "GBP"}},
	})
	if err != nil {
		t.Fatalf("CompleteSale on a generated variant: %v", err)
	}

	var lineVariant sql.NullString
	if err := sqlDB.QueryRow(`SELECT variant_id FROM sale_lines WHERE sale_id = ?`, saleID).Scan(&lineVariant); err != nil {
		t.Fatalf("read sale line: %v", err)
	}
	if !lineVariant.Valid || lineVariant.String != variantID {
		t.Fatalf("sale line must reference the generated variant %s, got %+v", variantID, lineVariant)
	}
	var qty float64
	if err := sqlDB.QueryRow(`SELECT quantity FROM inventory WHERE variant_id = ? AND location_id = 'loc1'`, variantID).Scan(&qty); err != nil {
		t.Fatal(err)
	}
	if qty != 4 {
		t.Fatalf("expected the generated variant's stock to drop 5 -> 4, got %v", qty)
	}
}
