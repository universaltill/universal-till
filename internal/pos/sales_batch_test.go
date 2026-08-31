package pos

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/money"
)

// ut-docs#1318: CompleteSale's per-line writes are now batched. These tests
// pin the observable behavior the batching must NOT change: row counts, the
// final aggregated inventory when the SAME item appears on several lines,
// the negative-inventory guard (including its deliberate per-line-check
// quirk), and the empty-modifiers/empty-discounts no-op path.

// A multi-line sale with the same item on two lines must aggregate the stock
// decrement correctly, write one sale_lines/stock_movements row per line, one
// modifier row per chosen modifier, and one discount row per discount.
func TestCompleteSale_MultiLineRepeatedItemBatchedWrites(t *testing.T) {
	ctx := context.Background()
	db := setupSaleDB(t)
	defer db.Close()

	_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','SKU1','Apple', 500, 1)`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm2','SKU2','Coffee', 300, 1)`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm3','SKU3','Bread', 200, 1)`)
	_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv1','itm1',NULL,'loc1',10,datetime('now'))`)
	_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv2','itm2',NULL,'loc1',10,datetime('now'))`)
	_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv3','itm3',NULL,'loc1',10,datetime('now'))`)
	_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('cash','Cash','cash',1)`)

	in := SaleInput{
		SaleType:     "sale",
		RegisterID:   "reg1",
		CashierID:    "user1",
		Currency:     "GBP",
		TaxInclusive: false,
		SaleDiscount: money.FromMinor(50),
		Lines: []SaleLineInput{
			{ItemID: "itm1", SKU: "SKU1", Name: "Apple", Qty: 2, UnitPrice: money.FromMinor(500), TaxRateBasisPoints: 2000, LocationID: "loc1"},
			{ItemID: "itm2", SKU: "SKU2", Name: "Coffee", Qty: 1, UnitPrice: money.FromMinor(300), TaxRateBasisPoints: 2000, LocationID: "loc1",
				LineDiscount: money.FromMinor(30),
				Modifiers: []data.SelectedModifier{
					{GroupID: "g1", OptionID: "o1", GroupName: "Size", OptionName: "Large", PriceDeltaMinor: 50},
					{GroupName: "Milk", OptionName: "Oat", PriceDeltaMinor: 30},
				}},
			// SAME item as line 1 again — the combined decrement must land.
			{ItemID: "itm1", SKU: "SKU1", Name: "Apple", Qty: 3, UnitPrice: money.FromMinor(500), TaxRateBasisPoints: 2000, LocationID: "loc1"},
			{ItemID: "itm3", SKU: "SKU3", Name: "Bread", Qty: 1, UnitPrice: money.FromMinor(200), TaxRateBasisPoints: 2000, LocationID: "loc1"},
		},
		Payments: []PaymentInput{
			// Generous cash overpayment with no change: coverage is all the
			// completion path requires.
			{MethodID: "cash", Amount: money.FromMinor(10000), Currency: "GBP"},
		},
		AllowNegativeInventory: false,
	}

	saleID, err := CompleteSale(ctx, db, in)
	if err != nil {
		t.Fatalf("CompleteSale: %v", err)
	}

	counts := map[string]int{}
	for table, query := range map[string]string{
		"sale_lines":          `SELECT COUNT(*) FROM sale_lines WHERE sale_id = ?`,
		"sale_line_modifiers": `SELECT COUNT(*) FROM sale_line_modifiers m JOIN sale_lines l ON l.id = m.sale_line_id WHERE l.sale_id = ?`,
		"sale_discounts":      `SELECT COUNT(*) FROM sale_discounts WHERE sale_id = ?`,
		"stock_movements":     `SELECT COUNT(*) FROM stock_movements m JOIN sale_lines l ON l.id = m.sale_line_id WHERE l.sale_id = ?`,
	} {
		var c int
		if err := db.QueryRow(query, saleID).Scan(&c); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		counts[table] = c
	}
	if counts["sale_lines"] != 4 {
		t.Errorf("sale_lines = %d, want 4", counts["sale_lines"])
	}
	if counts["sale_line_modifiers"] != 2 {
		t.Errorf("sale_line_modifiers = %d, want 2", counts["sale_line_modifiers"])
	}
	// One sale-level + one line-level discount.
	if counts["sale_discounts"] != 2 {
		t.Errorf("sale_discounts = %d, want 2", counts["sale_discounts"])
	}
	// Every stock movement must reference a real sale line of THIS sale
	// (the join proves sale_line_id survived batching).
	if counts["stock_movements"] != 4 {
		t.Errorf("stock_movements linked to sale lines = %d, want 4", counts["stock_movements"])
	}

	// Line numbering preserved in input order.
	var lineNo int
	var name string
	if err := db.QueryRow(`SELECT line_no, name_snapshot FROM sale_lines WHERE sale_id = ? AND line_no = 3`, saleID).Scan(&lineNo, &name); err != nil {
		t.Fatalf("line 3: %v", err)
	}
	if name != "Apple" {
		t.Errorf("line 3 name = %q, want Apple", name)
	}

	// Final inventory, read back through the batch reader itself: itm1 sold
	// on TWO lines (2 + 3) → 10-5=5; itm2 10-1=9; itm3 10-1=9.
	repo := data.NewPOSRepo(db)
	k1 := data.StockKey{LocationID: "loc1", ItemID: "itm1"}
	k2 := data.StockKey{LocationID: "loc1", ItemID: "itm2"}
	k3 := data.StockKey{LocationID: "loc1", ItemID: "itm3"}
	qtys, err := repo.CurrentQtyBatch(ctx, nil, []data.StockKey{k1, k2, k3})
	if err != nil {
		t.Fatalf("CurrentQtyBatch: %v", err)
	}
	if qtys[k1] != 5 {
		t.Errorf("itm1 final qty = %v, want 5 (combined decrement across two lines)", qtys[k1])
	}
	if qtys[k2] != 9 {
		t.Errorf("itm2 final qty = %v, want 9", qtys[k2])
	}
	if qtys[k3] != 9 {
		t.Errorf("itm3 final qty = %v, want 9", qtys[k3])
	}

	// One audit_log inventory row per movement (per line), not per item.
	var auditRows int
	_ = db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE entity_type='inventory'`).Scan(&auditRows)
	if auditRows != 4 {
		t.Errorf("audit_log inventory rows = %d, want 4 (one per movement)", auditRows)
	}
}

// The negative-inventory guard's exact existing semantics, preserved:
//   - a single line asking for more than the current stock fails the sale;
//   - two lines that only TOGETHER oversell still pass, because each line is
//     checked independently against the same pre-sale quantity (pre-existing
//     quirk, deliberately not fixed by the ut-docs#1318 batching — the batch
//     check must not be stricter than the loop it replaced);
//   - AllowNegativeInventory bypasses the guard entirely.
func TestCompleteSale_NegativeInventoryGuardSemanticsPreserved(t *testing.T) {
	ctx := context.Background()

	baseInput := func(lines []SaleLineInput) SaleInput {
		return SaleInput{
			SaleType:   "sale",
			RegisterID: "reg1",
			CashierID:  "user1",
			Currency:   "GBP",
			Lines:      lines,
			Payments: []PaymentInput{
				{MethodID: "cash", Amount: money.FromMinor(100000), Currency: "GBP"},
			},
		}
	}

	t.Run("single line overselling fails", func(t *testing.T) {
		db := setupSaleDB(t)
		defer db.Close()
		_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`)
		_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','SKU1','Apple', 500, 1)`)
		_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv1','itm1',NULL,'loc1',4,datetime('now'))`)
		_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('cash','Cash','cash',1)`)

		in := baseInput([]SaleLineInput{
			{ItemID: "itm1", Name: "Apple", Qty: 5, UnitPrice: money.FromMinor(500), TaxRateBasisPoints: 2000, LocationID: "loc1"},
		})
		if _, err := CompleteSale(ctx, db, in); err == nil {
			t.Fatal("expected insufficient-stock error, got nil")
		}
		// The failed sale must leave nothing behind.
		var count int
		_ = db.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&count)
		if count != 0 {
			t.Fatalf("failed sale left %d sales rows", count)
		}
		var qty float64
		_ = db.QueryRow(`SELECT quantity FROM inventory WHERE item_id='itm1'`).Scan(&qty)
		if qty != 4 {
			t.Fatalf("failed sale changed inventory to %v, want 4", qty)
		}
	})

	t.Run("two lines overselling only in combination still pass (existing quirk)", func(t *testing.T) {
		db := setupSaleDB(t)
		defer db.Close()
		_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`)
		_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','SKU1','Apple', 500, 1)`)
		_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv1','itm1',NULL,'loc1',4,datetime('now'))`)
		_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('cash','Cash','cash',1)`)

		in := baseInput([]SaleLineInput{
			{ItemID: "itm1", Name: "Apple", Qty: 3, UnitPrice: money.FromMinor(500), TaxRateBasisPoints: 2000, LocationID: "loc1"},
			{ItemID: "itm1", Name: "Apple", Qty: 3, UnitPrice: money.FromMinor(500), TaxRateBasisPoints: 2000, LocationID: "loc1"},
		})
		if _, err := CompleteSale(ctx, db, in); err != nil {
			t.Fatalf("per-line check must pass 3+3 against stock 4 (each line sees 4): %v", err)
		}
		var qty float64
		_ = db.QueryRow(`SELECT quantity FROM inventory WHERE item_id='itm1'`).Scan(&qty)
		if qty != -2 {
			t.Fatalf("final inventory = %v, want -2 (4 - 3 - 3)", qty)
		}
	})

	t.Run("AllowNegativeInventory bypasses the guard", func(t *testing.T) {
		db := setupSaleDB(t)
		defer db.Close()
		_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`)
		_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','SKU1','Apple', 500, 1)`)
		_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv1','itm1',NULL,'loc1',4,datetime('now'))`)
		_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('cash','Cash','cash',1)`)

		in := baseInput([]SaleLineInput{
			{ItemID: "itm1", Name: "Apple", Qty: 9, UnitPrice: money.FromMinor(500), TaxRateBasisPoints: 2000, LocationID: "loc1"},
		})
		in.AllowNegativeInventory = true
		if _, err := CompleteSale(ctx, db, in); err != nil {
			t.Fatalf("CompleteSale with AllowNegativeInventory: %v", err)
		}
		var qty float64
		_ = db.QueryRow(`SELECT quantity FROM inventory WHERE item_id='itm1'`).Scan(&qty)
		if qty != -5 {
			t.Fatalf("final inventory = %v, want -5", qty)
		}
	})

	t.Run("item with no inventory row fails when overselling", func(t *testing.T) {
		db := setupSaleDB(t)
		defer db.Close()
		_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`)
		_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm9','SKU9','Ghost', 500, 1)`)
		_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('cash','Cash','cash',1)`)

		in := baseInput([]SaleLineInput{
			{ItemID: "itm9", Name: "Ghost", Qty: 1, UnitPrice: money.FromMinor(500), TaxRateBasisPoints: 2000, LocationID: "loc1"},
		})
		// Missing inventory row reads as 0 (same as CurrentQty found=false).
		if _, err := CompleteSale(ctx, db, in); err == nil {
			t.Fatal("expected insufficient-stock error for item with no inventory row")
		}
	})
}

// A sale whose lines carry no modifiers and no discounts must sail through
// the empty-slice no-op batch paths.
func TestCompleteSale_NoModifiersNoDiscounts(t *testing.T) {
	ctx := context.Background()
	db := setupSaleDB(t)
	defer db.Close()

	_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','SKU1','Apple', 500, 1)`)
	_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv1','itm1',NULL,'loc1',5,datetime('now'))`)
	_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('cash','Cash','cash',1)`)

	in := SaleInput{
		SaleType:   "sale",
		RegisterID: "reg1",
		CashierID:  "user1",
		Currency:   "GBP",
		Lines: []SaleLineInput{
			{ItemID: "itm1", Name: "Apple", Qty: 1, UnitPrice: money.FromMinor(500), TaxRateBasisPoints: 2000, LocationID: "loc1"},
		},
		Payments: []PaymentInput{
			{MethodID: "cash", Amount: money.FromMinor(600), Currency: "GBP"},
		},
	}
	if _, err := CompleteSale(ctx, db, in); err != nil {
		t.Fatalf("CompleteSale with zero modifiers/discounts: %v", err)
	}
	var count int
	_ = db.QueryRow(`SELECT COUNT(*) FROM sale_line_modifiers`).Scan(&count)
	if count != 0 {
		t.Errorf("sale_line_modifiers = %d, want 0", count)
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM sale_discounts`).Scan(&count)
	if count != 0 {
		t.Errorf("sale_discounts = %d, want 0", count)
	}
}

// Returns move stock the OTHER way; the batched movement path must keep the
// positive quantity for a return.
func TestCompleteSale_ReturnRestocksThroughBatchedMovements(t *testing.T) {
	ctx := context.Background()
	db := setupSaleDB(t)
	defer db.Close()

	_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','SKU1','Apple', 500, 1)`)
	_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv1','itm1',NULL,'loc1',5,datetime('now'))`)
	_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('cash','Cash','cash',1)`)

	in := SaleInput{
		SaleType:   "return",
		RegisterID: "reg1",
		CashierID:  "user1",
		Currency:   "GBP",
		Lines: []SaleLineInput{
			{ItemID: "itm1", Name: "Apple", Qty: 2, UnitPrice: money.FromMinor(500), TaxRateBasisPoints: 2000, LocationID: "loc1"},
		},
		Payments: []PaymentInput{
			{MethodID: "cash", Amount: money.FromMinor(1200), Currency: "GBP"},
		},
	}
	if _, err := CompleteSale(ctx, db, in); err != nil {
		t.Fatalf("CompleteSale(return): %v", err)
	}
	var qty float64
	_ = db.QueryRow(`SELECT quantity FROM inventory WHERE item_id='itm1'`).Scan(&qty)
	if qty != 7 {
		t.Fatalf("inventory after return = %v, want 7", qty)
	}
}
