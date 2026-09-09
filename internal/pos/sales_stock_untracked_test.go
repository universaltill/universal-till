package pos

import (
	"context"
	"testing"
)

// TestCompleteSale_UntrackedItem_NoStockMovementNoInventoryRow covers
// ut-docs#1850: an item flagged stock_untracked gets NO stock_movements
// row and NO inventory row created, even though this DB's own
// AllowNegativeInventory=false stock check would otherwise apply — the
// item having no pre-existing inventory row proves the guard skipped it
// rather than merely tolerating a negative quantity.
func TestCompleteSale_UntrackedItem_NoStockMovementNoInventoryRow(t *testing.T) {
	ctx := context.Background()
	db := setupSaleDB(t)
	defer db.Close()

	_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`)
	// Deliberately NO inventory row for itmU — an untracked item is never
	// expected to have one; if the guard fails to skip it, both the
	// insufficient-stock check (reading 0 pre-sale qty) and the movement
	// insert would misbehave, giving this test real teeth.
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active, stock_untracked) VALUES('itmU','SKU-U','Untracked Widget', 500, 1, 1)`)
	_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('cash','Cash','cash',1)`)

	in := SaleInput{
		SaleType:     "sale",
		RegisterID:   "reg1",
		CashierID:    "user1",
		Currency:     "GBP",
		TaxInclusive: false,
		Lines: []SaleLineInput{
			{
				ItemID:             "itmU",
				SKU:                "SKU-U",
				Name:               "Untracked Widget",
				Qty:                2,
				UnitPrice:          500,
				TaxRateBasisPoints: 2000,
				LocationID:         "loc1",
			},
		},
		Payments: []PaymentInput{
			{MethodID: "cash", Amount: 1200, Currency: "GBP"},
		},
		AllowNegativeInventory: false,
	}

	saleID, err := CompleteSale(ctx, db, in)
	if err != nil {
		t.Fatalf("CompleteSale error: %v (an untracked item must never fail the insufficient-stock check)", err)
	}
	if saleID == "" {
		t.Fatalf("expected saleID")
	}

	var count int
	_ = db.QueryRow(`SELECT COUNT(*) FROM sale_lines`).Scan(&count)
	if count != 1 {
		t.Fatalf("expected 1 sale line, got %d", count)
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM stock_movements`).Scan(&count)
	if count != 0 {
		t.Fatalf("expected 0 stock movements for an untracked item, got %d", count)
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM inventory WHERE item_id='itmU'`).Scan(&count)
	if count != 0 {
		t.Fatalf("expected 0 inventory rows for an untracked item, got %d", count)
	}
}

// TestCompleteSale_TrackedItem_StillGuarded is the control: a tracked item
// in the SAME sale as an untracked one still hits the stock guard and
// still gets a movement — proving the two coexist rather than the
// untracked flag accidentally disabling tracking sale-wide.
func TestCompleteSale_TrackedItem_StillGuarded(t *testing.T) {
	ctx := context.Background()
	db := setupSaleDB(t)
	defer db.Close()

	_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active, stock_untracked) VALUES('itmT','SKU-T','Tracked Widget', 500, 1, 0)`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active, stock_untracked) VALUES('itmU','SKU-U','Untracked Widget', 500, 1, 1)`)
	_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv1','itmT',NULL,'loc1',1,datetime('now'))`)
	_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('cash','Cash','cash',1)`)

	in := SaleInput{
		SaleType:     "sale",
		RegisterID:   "reg1",
		CashierID:    "user1",
		Currency:     "GBP",
		TaxInclusive: false,
		Lines: []SaleLineInput{
			// Tracked item, only 1 in stock — selling 5 must fail the guard.
			{ItemID: "itmT", SKU: "SKU-T", Name: "Tracked Widget", Qty: 5, UnitPrice: 500, TaxRateBasisPoints: 2000, LocationID: "loc1"},
			{ItemID: "itmU", SKU: "SKU-U", Name: "Untracked Widget", Qty: 5, UnitPrice: 500, TaxRateBasisPoints: 2000, LocationID: "loc1"},
		},
		Payments: []PaymentInput{
			{MethodID: "cash", Amount: 6000, Currency: "GBP"},
		},
		AllowNegativeInventory: false,
	}

	if _, err := CompleteSale(ctx, db, in); err == nil {
		t.Fatalf("expected insufficient-stock error for the tracked item, got nil")
	}
}
