package pos

// ut-docs#1318 — batched CompleteSale writes. These are characterization
// tests for the tender transaction's per-line persistence: they pin the
// EXACT current behavior (including the pre-existing same-initial-quantity
// stock-check quirk) so the batching change can be verified to be a pure
// batching change. They pass against the unbatched code by design; the
// batched code must keep them passing unchanged.

import (
	"context"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// TestCompleteSale_MultiLineBasketWritesEverything drives one basket through
// every per-line write type at once: a shared item across two lines, a line
// discount, a line with modifiers, and a variant line.
func TestCompleteSale_MultiLineBasketWritesEverything(t *testing.T) {
	ctx := context.Background()
	db := setupSaleDB(t)
	defer db.Close()

	_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','SKU1','Apple', 500, 1)`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm2','SKU2','Coffee', 300, 1)`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm3','SKU3','Cake', 400, 1)`)
	_, _ = db.Exec(`INSERT INTO item_variants(id, item_id, price, is_active) VALUES('var1','itm3', 450, 1)`)
	_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv1','itm1',NULL,'loc1',100,datetime('now'))`)
	_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv2','itm2',NULL,'loc1',100,datetime('now'))`)
	_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv3',NULL,'var1','loc1',100,datetime('now'))`)
	_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('cash','Cash','cash',1)`)

	in := SaleInput{
		SaleType:     "sale",
		RegisterID:   "reg1",
		CashierID:    "user1",
		Currency:     "GBP",
		TaxInclusive: false,
		Lines: []SaleLineInput{
			// Shared item, two separate lines.
			{ItemID: "itm1", SKU: "SKU1", Name: "Apple", Qty: 2, UnitPrice: 500, TaxRateBasisPoints: 2000, LocationID: "loc1"},
			{ItemID: "itm1", SKU: "SKU1", Name: "Apple", Qty: 3, UnitPrice: 500, TaxRateBasisPoints: 2000, LocationID: "loc1"},
			// Line discount.
			{ItemID: "itm2", SKU: "SKU2", Name: "Coffee", Qty: 1, UnitPrice: 300, TaxRateBasisPoints: 2000, LineDiscount: 100, LocationID: "loc1"},
			// Modifiers.
			{ItemID: "itm2", SKU: "SKU2", Name: "Coffee", Qty: 1, UnitPrice: 350, TaxRateBasisPoints: 2000, LocationID: "loc1",
				Modifiers: []data.SelectedModifier{
					{GroupID: "g1", OptionID: "o1", GroupName: "Extras", OptionName: "Extra shot", PriceDeltaMinor: 50},
					{GroupName: "Milk", OptionName: "Oat", PriceDeltaMinor: 0},
				}},
			// Variant line.
			{VariantID: "var1", SKU: "SKU3-L", Name: "Cake Large", Qty: 1, UnitPrice: 450, TaxRateBasisPoints: 2000, LocationID: "loc1"},
		},
		// subtotal: 1000+1500+(300-100)+350+450 = 3500; tax 20% = 700; total 4200.
		Payments:               []PaymentInput{{MethodID: "cash", Amount: 4200, Currency: "GBP"}},
		AllowNegativeInventory: false,
	}

	saleID, err := CompleteSale(ctx, db, in)
	if err != nil {
		t.Fatalf("CompleteSale error: %v", err)
	}

	var count int
	_ = db.QueryRow(`SELECT COUNT(*) FROM sale_lines WHERE sale_id=?`, saleID).Scan(&count)
	if count != 5 {
		t.Fatalf("expected 5 sale lines, got %d", count)
	}
	// line_no is 1-based input order.
	var lineNos []int
	rows, err := db.Query(`SELECT line_no FROM sale_lines WHERE sale_id=? ORDER BY line_no`, saleID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var n int
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		lineNos = append(lineNos, n)
	}
	rows.Close()
	for i, n := range lineNos {
		if n != i+1 {
			t.Fatalf("line_no sequence broken: %v", lineNos)
		}
	}

	_ = db.QueryRow(`SELECT COUNT(*) FROM stock_movements WHERE type='sale'`).Scan(&count)
	if count != 5 {
		t.Fatalf("expected 5 stock movements (one per line, even shared items), got %d", count)
	}
	// Shared item deducted by BOTH lines' quantities: 100 - 2 - 3 = 95.
	var qty float64
	_ = db.QueryRow(`SELECT quantity FROM inventory WHERE item_id='itm1' AND location_id='loc1'`).Scan(&qty)
	if qty != 95 {
		t.Fatalf("shared item inventory: got %v, want 95", qty)
	}
	_ = db.QueryRow(`SELECT quantity FROM inventory WHERE variant_id='var1' AND location_id='loc1'`).Scan(&qty)
	if qty != 99 {
		t.Fatalf("variant inventory: got %v, want 99", qty)
	}

	// Exactly one line discount row, attached to the right line.
	var lineID string
	if err := db.QueryRow(`SELECT id FROM sale_lines WHERE sale_id=? AND line_no=3`, saleID).Scan(&lineID); err != nil {
		t.Fatal(err)
	}
	var amount int64
	var reason string
	if err := db.QueryRow(`SELECT amount, reason FROM sale_discounts WHERE sale_id=? AND line_id=?`, saleID, lineID).Scan(&amount, &reason); err != nil {
		t.Fatalf("line discount row: %v", err)
	}
	if amount != 100 || reason != "line_discount" {
		t.Fatalf("line discount wrong: amount=%d reason=%q", amount, reason)
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM sale_discounts WHERE sale_id=?`, saleID).Scan(&count)
	if count != 1 {
		t.Fatalf("expected exactly 1 discount row, got %d", count)
	}

	// Modifiers landed on the right line only.
	if err := db.QueryRow(`SELECT id FROM sale_lines WHERE sale_id=? AND line_no=4`, saleID).Scan(&lineID); err != nil {
		t.Fatal(err)
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM sale_line_modifiers WHERE sale_line_id=?`, lineID).Scan(&count)
	if count != 2 {
		t.Fatalf("expected 2 modifier rows on line 4, got %d", count)
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM sale_line_modifiers`).Scan(&count)
	if count != 2 {
		t.Fatalf("expected 2 modifier rows total, got %d", count)
	}

	// One inventory audit row per line's movement, plus the sale audit row.
	_ = db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE entity_type='inventory'`).Scan(&count)
	if count != 5 {
		t.Fatalf("expected 5 inventory audit rows, got %d", count)
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE entity_type='sale' AND entity_id=?`, saleID).Scan(&count)
	if count != 1 {
		t.Fatalf("expected 1 sale audit row, got %d", count)
	}
}

// TestCompleteSale_SharedItemStockCheckUsesInitialQty pins the pre-existing
// stock-check characteristic the batching change must NOT "fix": every line
// is checked against the SAME initial current quantity — two lines of the
// same item do not see each other's depletion, so a basket whose lines
// TOGETHER exceed stock still completes (and drives inventory negative).
// Changing this is a separate product decision, not a batching side effect.
func TestCompleteSale_SharedItemStockCheckUsesInitialQty(t *testing.T) {
	ctx := context.Background()
	db := setupSaleDB(t)
	defer db.Close()

	_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','SKU1','Apple', 500, 1)`)
	_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv1','itm1',NULL,'loc1',5,datetime('now'))`)
	_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('cash','Cash','cash',1)`)

	in := SaleInput{
		SaleType: "sale", Currency: "GBP", TaxInclusive: false,
		Lines: []SaleLineInput{
			// Each line alone fits within 5; together they need 6.
			{ItemID: "itm1", SKU: "SKU1", Name: "Apple", Qty: 3, UnitPrice: 100, TaxRateBasisPoints: 0, LocationID: "loc1"},
			{ItemID: "itm1", SKU: "SKU1", Name: "Apple", Qty: 3, UnitPrice: 100, TaxRateBasisPoints: 0, LocationID: "loc1"},
		},
		Payments:               []PaymentInput{{MethodID: "cash", Amount: 600, Currency: "GBP"}},
		AllowNegativeInventory: false,
	}

	if _, err := CompleteSale(ctx, db, in); err != nil {
		t.Fatalf("pre-existing behavior: per-line check against the initial qty must ALLOW this sale, got %v", err)
	}
	var qty float64
	_ = db.QueryRow(`SELECT quantity FROM inventory WHERE item_id='itm1' AND location_id='loc1'`).Scan(&qty)
	if qty != -1 {
		t.Fatalf("inventory after over-sale: got %v, want -1 (5 - 3 - 3)", qty)
	}
}

// TestCompleteSale_InsufficientStockRollsBackEverything: a single line that
// would take stock negative must abort the WHOLE sale before any row is
// written — including the earlier, individually-sufficient lines' rows.
func TestCompleteSale_InsufficientStockRollsBackEverything(t *testing.T) {
	ctx := context.Background()
	db := setupSaleDB(t)
	defer db.Close()

	_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','SKU1','Apple', 500, 1)`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm2','SKU2','Pear', 300, 1)`)
	_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv1','itm1',NULL,'loc1',10,datetime('now'))`)
	_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv2','itm2',NULL,'loc1',1,datetime('now'))`)
	_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('cash','Cash','cash',1)`)

	in := SaleInput{
		SaleType: "sale", Currency: "GBP", TaxInclusive: false,
		Lines: []SaleLineInput{
			{ItemID: "itm1", SKU: "SKU1", Name: "Apple", Qty: 2, UnitPrice: 100, TaxRateBasisPoints: 0, LineDiscount: 10, LocationID: "loc1",
				Modifiers: []data.SelectedModifier{{GroupName: "Extras", OptionName: "Shot", PriceDeltaMinor: 0}}},
			{ItemID: "itm2", SKU: "SKU2", Name: "Pear", Qty: 5, UnitPrice: 100, TaxRateBasisPoints: 0, LocationID: "loc1"}, // only 1 in stock
		},
		Payments:               []PaymentInput{{MethodID: "cash", Amount: 2000, Currency: "GBP"}},
		AllowNegativeInventory: false,
	}

	_, err := CompleteSale(ctx, db, in)
	if err == nil {
		t.Fatal("expected insufficient stock error")
	}
	if !strings.Contains(err.Error(), "insufficient stock for item itm2") {
		t.Fatalf("wrong error: %v", err)
	}

	// NOTHING was written — the check aborts the transaction before any row.
	for _, table := range []string{"sales", "sale_lines", "sale_line_modifiers", "sale_discounts", "stock_movements", "payments", "audit_log"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("table %s has %d rows after failed sale, want 0", table, count)
		}
	}
	var qty float64
	_ = db.QueryRow(`SELECT quantity FROM inventory WHERE item_id='itm1'`).Scan(&qty)
	if qty != 10 {
		t.Fatalf("itm1 inventory touched: got %v, want 10", qty)
	}
	_ = db.QueryRow(`SELECT quantity FROM inventory WHERE item_id='itm2'`).Scan(&qty)
	if qty != 1 {
		t.Fatalf("itm2 inventory touched: got %v, want 1", qty)
	}
}
