package data

// ut-docs#1318 — batched CompleteSale writes. Tests for the repo-side batch
// methods CompleteSale's tender transaction uses instead of one exec per
// line per write type:
//
//   CurrentQtyBatch              — one SELECT for every line's stock check
//   InsertSaleLines              — one prepared statement across all lines
//   InsertSaleLineModifiersBatch — one prepared statement across all lines' modifiers
//   InsertSaleDiscounts          — one prepared statement across line discounts
//   RecordStockMovements         — the 4 per-movement statements prepared once
//
// Each batch method must reproduce its single-row sibling's semantics
// exactly (same NULL handling, same validation error strings, same
// inventory insert-on-missing branch) — the batching card's hard constraint
// is "no behavior change, only fewer statement compilations".
//
// Conventions follow pos_repo_batch8_inventory_test.go: real migrations via
// openB8InvDB, "b1318-" prefixed seed rows.

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
)

func seed1318Item(t *testing.T, d *db.DB, id string) {
	t.Helper()
	mustExec(t, d, `INSERT INTO items (id, sku, name, base_price, is_active) VALUES (?,?,?,100,1)`,
		id, "sku-"+id, "Item "+id)
}

func seed1318Inventory(t *testing.T, d *db.DB, itemID, locationID string, qty float64) {
	t.Helper()
	mustExec(t, d, `INSERT INTO inventory (id, item_id, variant_id, location_id, quantity) VALUES (?,?,NULL,?,?)`,
		"inv-"+itemID+"-"+locationID, itemID, locationID, qty)
}

func seed1318Variant(t *testing.T, d *db.DB, itemID, variantID, locationID string, qty float64) {
	t.Helper()
	seed1318Item(t, d, itemID)
	mustExec(t, d, `INSERT INTO item_variants (id, item_id, name, price) VALUES (?,?,?,150)`,
		variantID, itemID, "500ml")
	mustExec(t, d, `INSERT INTO inventory (id, item_id, variant_id, location_id, quantity) VALUES (?,NULL,?,?,?)`,
		"inv-"+variantID, variantID, locationID, qty)
}

// insert1318Sale writes a minimal valid sale header so line/discount rows
// can satisfy their sale_id FKs.
func insert1318Sale(t *testing.T, repo *POSRepo, tx *sql.Tx, saleID, receiptNo string) {
	t.Helper()
	if err := repo.InsertSale(context.Background(), tx, InsertSaleParams{
		SaleID: saleID, ReceiptNo: receiptNo, SaleType: "sale", Currency: "GBP",
		CreatedAt: "2026-08-31T10:00:00Z", SyncStatus: "synced", TenderType: "cash",
	}); err != nil {
		t.Fatalf("insert sale header: %v", err)
	}
}

func TestCurrentQtyBatch_1318(t *testing.T) {
	d, repo := openB8InvDB(t)
	ctx := context.Background()

	// Empty input: empty result, no query error.
	if got, err := repo.CurrentQtyBatch(ctx, nil, nil); err != nil || len(got) != 0 {
		t.Fatalf("empty keys: got %v err=%v, want empty map", got, err)
	}

	seed1318Item(t, d, "b1318-a")
	seed1318Inventory(t, d, "b1318-a", "loc_main", 7.5)
	seed1318Item(t, d, "b1318-b")
	seed1318Inventory(t, d, "b1318-b", "loc_main", 4)
	seed1318Variant(t, d, "b1318-vp", "b1318-v", "loc_back", 3)

	keys := []InventoryKey{
		{LocationID: "loc_main", ItemID: "b1318-a"},
		{LocationID: "loc_main", ItemID: "b1318-a"}, // duplicate (same item on two basket lines)
		{LocationID: "loc_main", ItemID: "b1318-b"},
		{LocationID: "loc_back", VariantID: "b1318-v"},
		{LocationID: "loc_main", ItemID: "b1318-ghost"}, // never stocked
		{LocationID: "loc_wh", ItemID: "b1318-a"},       // stocked, but not at this location
	}
	got, err := repo.CurrentQtyBatch(ctx, nil, keys)
	if err != nil {
		t.Fatalf("batch: %v", err)
	}
	if q, ok := got[InventoryKey{LocationID: "loc_main", ItemID: "b1318-a"}]; !ok || q != 7.5 {
		t.Fatalf("b1318-a@loc_main: got %v ok=%v, want 7.5", q, ok)
	}
	if q, ok := got[InventoryKey{LocationID: "loc_main", ItemID: "b1318-b"}]; !ok || q != 4 {
		t.Fatalf("b1318-b@loc_main: got %v ok=%v, want 4", q, ok)
	}
	if q, ok := got[InventoryKey{LocationID: "loc_back", VariantID: "b1318-v"}]; !ok || q != 3 {
		t.Fatalf("b1318-v@loc_back: got %v ok=%v, want 3", q, ok)
	}
	// Not-found semantics mirror CurrentQty's found=false: the key is simply
	// absent (the caller treats absent as qty 0).
	if _, ok := got[InventoryKey{LocationID: "loc_main", ItemID: "b1318-ghost"}]; ok {
		t.Fatalf("ghost item must be absent, got %v", got)
	}
	if _, ok := got[InventoryKey{LocationID: "loc_wh", ItemID: "b1318-a"}]; ok {
		t.Fatalf("wrong-location key must be absent, got %v", got)
	}

	// An item id passed in the variant slot must not match an item-level row
	// (CurrentQty's query requires exactly one of the two to match).
	got, err = repo.CurrentQtyBatch(ctx, nil, []InventoryKey{{LocationID: "loc_main", VariantID: "b1318-a"}})
	if err != nil || len(got) != 0 {
		t.Fatalf("item id in variant slot: got %v err=%v, want empty", got, err)
	}
	// And a variant id in the item slot must not match a variant-level row.
	got, err = repo.CurrentQtyBatch(ctx, nil, []InventoryKey{{LocationID: "loc_back", ItemID: "b1318-v"}})
	if err != nil || len(got) != 0 {
		t.Fatalf("variant id in item slot: got %v err=%v, want empty", got, err)
	}

	// Inside a transaction it reads uncommitted state, like CurrentQty.
	tx := b8Tx(t, d)
	if _, err := tx.Exec(`UPDATE inventory SET quantity = 1.25 WHERE item_id = 'b1318-a'`); err != nil {
		t.Fatalf("tx update: %v", err)
	}
	got, err = repo.CurrentQtyBatch(ctx, tx, []InventoryKey{{LocationID: "loc_main", ItemID: "b1318-a"}})
	if err != nil {
		t.Fatalf("in-tx batch: %v", err)
	}
	if q := got[InventoryKey{LocationID: "loc_main", ItemID: "b1318-a"}]; q != 1.25 {
		t.Fatalf("in-tx: got %v, want 1.25", q)
	}
}

// TestCurrentQtyBatch_MatchesCurrentQty_1318 pins parity: for every key,
// the batch answer must equal the single-row CurrentQty answer.
func TestCurrentQtyBatch_MatchesCurrentQty_1318(t *testing.T) {
	d, repo := openB8InvDB(t)
	ctx := context.Background()

	seed1318Item(t, d, "b1318-p1")
	seed1318Inventory(t, d, "b1318-p1", "loc_main", 12)
	seed1318Variant(t, d, "b1318-p2", "b1318-p2v", "loc_main", 0.5)

	keys := []InventoryKey{
		{LocationID: "loc_main", ItemID: "b1318-p1"},
		{LocationID: "loc_main", VariantID: "b1318-p2v"},
		{LocationID: "loc_back", ItemID: "b1318-p1"},
	}
	batch, err := repo.CurrentQtyBatch(ctx, nil, keys)
	if err != nil {
		t.Fatalf("batch: %v", err)
	}
	for _, k := range keys {
		single, found, err := repo.CurrentQty(ctx, nil, k.LocationID, k.ItemID, k.VariantID)
		if err != nil {
			t.Fatalf("single %v: %v", k, err)
		}
		bq, bfound := batch[k]
		if found != bfound {
			t.Fatalf("key %v: found mismatch single=%v batch=%v", k, found, bfound)
		}
		if found && single != bq {
			t.Fatalf("key %v: qty mismatch single=%v batch=%v", k, single, bq)
		}
	}
}

func TestInsertSaleLines_1318(t *testing.T) {
	d, repo := openB8InvDB(t)
	ctx := context.Background()

	// A transaction is required (the prepared statement lives on it).
	if err := repo.InsertSaleLines(ctx, nil, []SaleLineRow{{LineID: "x"}}); err == nil || !strings.Contains(err.Error(), "transaction required") {
		t.Fatalf("nil tx: want transaction required, got %v", err)
	}

	seed1318Variant(t, d, "b1318-slp", "b1318-slv", "loc_main", 5)

	tx, err := d.DB.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()
	insert1318Sale(t, repo, tx, "b1318-sale1", "b1318-r1")

	// Empty batch is a no-op.
	if err := repo.InsertSaleLines(ctx, tx, nil); err != nil {
		t.Fatalf("empty batch: %v", err)
	}

	rows := []SaleLineRow{
		{LineID: "b1318-l1", SaleID: "b1318-sale1", LineNo: 1, ItemID: "b1318-slp", Name: "Item A", SKU: "sku-a", Barcode: "bc-a", Qty: 2, UnitPrice: 500, LineDiscount: 0, TaxRateBP: 2000, TaxAmount: 200, TotalBeforeTax: 1000, TotalAfterTax: 1200},
		{LineID: "b1318-l2", SaleID: "b1318-sale1", LineNo: 2, VariantID: "b1318-slv", Name: "Item B", Qty: 0.5, UnitPrice: 300, LineDiscount: 50, TaxRateBP: 700, TaxAmount: 7, TotalBeforeTax: 100, TotalAfterTax: 107},
	}
	if err := repo.InsertSaleLines(ctx, tx, rows); err != nil {
		t.Fatalf("batch insert: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	var count int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM sale_lines WHERE sale_id='b1318-sale1'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("row count: got %d err=%v, want 2", count, err)
	}
	var itemID, variantID sql.NullString
	var name, skuS, bcS string
	var qty float64
	var unitPrice, lineDiscount, taxAmount, tbt, tat int64
	var taxBP, lineNo int
	if err := d.DB.QueryRow(`SELECT line_no, item_id, variant_id, name_snapshot, sku_snapshot, barcode_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax FROM sale_lines WHERE id='b1318-l1'`).
		Scan(&lineNo, &itemID, &variantID, &name, &skuS, &bcS, &qty, &unitPrice, &lineDiscount, &taxBP, &taxAmount, &tbt, &tat); err != nil {
		t.Fatalf("read l1: %v", err)
	}
	if lineNo != 1 || !itemID.Valid || itemID.String != "b1318-slp" || variantID.Valid ||
		name != "Item A" || skuS != "sku-a" || bcS != "bc-a" || qty != 2 ||
		unitPrice != 500 || lineDiscount != 0 || taxBP != 2000 || taxAmount != 200 || tbt != 1000 || tat != 1200 {
		t.Fatalf("l1 fields wrong: lineNo=%d item=%v variant=%v name=%q sku=%q bc=%q qty=%v up=%d ld=%d bp=%d tax=%d tbt=%d tat=%d",
			lineNo, itemID, variantID, name, skuS, bcS, qty, unitPrice, lineDiscount, taxBP, taxAmount, tbt, tat)
	}
	// Variant line: item_id NULL, variant_id set — the same nullIfEmpty
	// treatment InsertSaleLine applies.
	if err := d.DB.QueryRow(`SELECT item_id, variant_id FROM sale_lines WHERE id='b1318-l2'`).Scan(&itemID, &variantID); err != nil {
		t.Fatalf("read l2: %v", err)
	}
	if itemID.Valid || !variantID.Valid || variantID.String != "b1318-slv" {
		t.Fatalf("l2 identity wrong: item=%v variant=%v", itemID, variantID)
	}
}

func TestInsertSaleLineModifiersBatch_1318(t *testing.T) {
	d, repo := openB8InvDB(t)
	ctx := context.Background()

	seed1318Item(t, d, "b1318-mi")

	tx, err := d.DB.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()
	insert1318Sale(t, repo, tx, "b1318-sale2", "b1318-r2")
	if err := repo.InsertSaleLines(ctx, tx, []SaleLineRow{
		{LineID: "b1318-m-l1", SaleID: "b1318-sale2", LineNo: 1, ItemID: "b1318-mi", Name: "A", Qty: 1, UnitPrice: 100, TaxRateBP: 0, TotalBeforeTax: 100, TotalAfterTax: 100},
		{LineID: "b1318-m-l2", SaleID: "b1318-sale2", LineNo: 2, ItemID: "b1318-mi", Name: "B", Qty: 1, UnitPrice: 100, TaxRateBP: 0, TotalBeforeTax: 100, TotalAfterTax: 100},
		{LineID: "b1318-m-l3", SaleID: "b1318-sale2", LineNo: 3, ItemID: "b1318-mi", Name: "C", Qty: 1, UnitPrice: 100, TaxRateBP: 0, TotalBeforeTax: 100, TotalAfterTax: 100},
	}); err != nil {
		t.Fatalf("insert lines: %v", err)
	}

	// All-empty sets: a no-op (must not even need a prepared statement).
	if err := repo.InsertSaleLineModifiersBatch(ctx, tx, []SaleLineModifierSet{
		{LineID: "b1318-m-l1"}, {LineID: "b1318-m-l2"},
	}); err != nil {
		t.Fatalf("all-empty: %v", err)
	}

	sets := []SaleLineModifierSet{
		{LineID: "b1318-m-l1", Modifiers: []SelectedModifier{
			{GroupID: "g1", OptionID: "o1", GroupName: "Extras", OptionName: "Extra shot", PriceDeltaMinor: 50},
			{GroupName: "Milk", OptionName: "Oat", PriceDeltaMinor: 30}, // deleted source group/option → NULL ids
		}},
		{LineID: "b1318-m-l2"}, // line with no modifiers, interleaved
		{LineID: "b1318-m-l3", Modifiers: []SelectedModifier{
			{GroupID: "g2", OptionID: "o2", GroupName: "Size", OptionName: "Large", PriceDeltaMinor: 0},
		}},
	}
	if err := repo.InsertSaleLineModifiersBatch(ctx, tx, sets); err != nil {
		t.Fatalf("batch: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	var count int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM sale_line_modifiers WHERE sale_line_id IN ('b1318-m-l1','b1318-m-l2','b1318-m-l3')`).Scan(&count); err != nil || count != 3 {
		t.Fatalf("total rows: got %d err=%v, want 3", count, err)
	}
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM sale_line_modifiers WHERE sale_line_id='b1318-m-l2'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("no-modifier line rows: got %d err=%v, want 0", count, err)
	}
	var groupID, optionID sql.NullString
	var groupName, optionName string
	var delta int64
	if err := d.DB.QueryRow(`SELECT group_id, option_id, group_name_snapshot, option_name_snapshot, price_delta_minor FROM sale_line_modifiers WHERE sale_line_id='b1318-m-l1' AND option_name_snapshot='Oat'`).
		Scan(&groupID, &optionID, &groupName, &optionName, &delta); err != nil {
		t.Fatalf("read oat row: %v", err)
	}
	if groupID.Valid || optionID.Valid || groupName != "Milk" || delta != 30 {
		t.Fatalf("oat row wrong: gid=%v oid=%v group=%q delta=%d", groupID, optionID, groupName, delta)
	}
}

func TestInsertSaleDiscounts_1318(t *testing.T) {
	d, repo := openB8InvDB(t)
	ctx := context.Background()

	seed1318Item(t, d, "b1318-di")

	tx, err := d.DB.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()
	insert1318Sale(t, repo, tx, "b1318-sale3", "b1318-r3")
	if err := repo.InsertSaleLines(ctx, tx, []SaleLineRow{
		{LineID: "b1318-d-l1", SaleID: "b1318-sale3", LineNo: 1, ItemID: "b1318-di", Name: "A", Qty: 1, UnitPrice: 100, TaxRateBP: 0, TotalBeforeTax: 100, TotalAfterTax: 100},
	}); err != nil {
		t.Fatalf("insert line: %v", err)
	}

	// Empty batch is a no-op.
	if err := repo.InsertSaleDiscounts(ctx, tx, nil); err != nil {
		t.Fatalf("empty batch: %v", err)
	}

	rows := []SaleDiscountRow{
		{ID: "b1318-disc1", SaleID: "b1318-sale3", LineID: "b1318-d-l1", Type: "fixed", Value: 25, Amount: 25, Reason: "line_discount"},
		{ID: "b1318-disc2", SaleID: "b1318-sale3", Type: "fixed", Value: 100, Amount: 100, Reason: "sale_discount"}, // no line → NULL line_id
	}
	if err := repo.InsertSaleDiscounts(ctx, tx, rows); err != nil {
		t.Fatalf("batch: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	var lineID sql.NullString
	var dtype, reason string
	var value, amount int64
	if err := d.DB.QueryRow(`SELECT line_id, type, value, amount, reason FROM sale_discounts WHERE id='b1318-disc1'`).
		Scan(&lineID, &dtype, &value, &amount, &reason); err != nil {
		t.Fatalf("read disc1: %v", err)
	}
	if !lineID.Valid || lineID.String != "b1318-d-l1" || dtype != "fixed" || value != 25 || amount != 25 || reason != "line_discount" {
		t.Fatalf("disc1 wrong: line=%v type=%q value=%d amount=%d reason=%q", lineID, dtype, value, amount, reason)
	}
	if err := d.DB.QueryRow(`SELECT line_id FROM sale_discounts WHERE id='b1318-disc2'`).Scan(&lineID); err != nil {
		t.Fatalf("read disc2: %v", err)
	}
	if lineID.Valid {
		t.Fatalf("sale-level discount must have NULL line_id, got %v", lineID)
	}
}

func TestRecordStockMovements_1318(t *testing.T) {
	d, repo := openB8InvDB(t)
	ctx := context.Background()

	// A transaction is required — unlike RecordStockMovement, the batch
	// variant never opens its own (CompleteSale always already has one).
	if _, err := repo.RecordStockMovements(ctx, nil, []StockMovementInput{{ItemID: "x", LocationID: "loc_main", Type: "sale", Quantity: -1}}); err == nil || !strings.Contains(err.Error(), "transaction required") {
		t.Fatalf("nil tx: want transaction required, got %v", err)
	}

	seed1318Item(t, d, "b1318-sm1")
	seed1318Inventory(t, d, "b1318-sm1", "loc_main", 10)
	seed1318Item(t, d, "b1318-sm2") // deliberately NO inventory row → insert branch
	seed1318Variant(t, d, "b1318-smp", "b1318-smv", "loc_main", 5)

	// Validation mirrors RecordStockMovement's, same error strings.
	tx := b8Tx(t, d)
	for _, tc := range []struct {
		in   StockMovementInput
		want string
	}{
		{StockMovementInput{ItemID: "i", Type: "sale", Quantity: -1}, "locationID required"},
		{StockMovementInput{LocationID: "loc_main", Type: "sale", Quantity: -1}, "itemID or variantID required"},
		{StockMovementInput{ItemID: "i", VariantID: "v", LocationID: "loc_main", Type: "sale", Quantity: -1}, "cannot specify both itemID and variantID"},
		{StockMovementInput{ItemID: "i", LocationID: "loc_main", Quantity: -1}, "type required"},
		{StockMovementInput{ItemID: "i", LocationID: "loc_main", Type: "sale"}, "quantity must be non-zero"},
	} {
		if _, err := repo.RecordStockMovements(ctx, tx, []StockMovementInput{tc.in}); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("validation %+v: want %q, got %v", tc.in, tc.want, err)
		}
	}

	// Empty batch is a no-op.
	if ids, err := repo.RecordStockMovements(ctx, tx, nil); err != nil || len(ids) != 0 {
		t.Fatalf("empty batch: ids=%v err=%v", ids, err)
	}
	// Release the validation tx before opening the write tx (single-writer
	// SQLite; the b8Tx cleanup rollback only runs at test end).
	_ = tx.Rollback()

	tx2, err := d.DB.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx2.Rollback()
	ins := []StockMovementInput{
		{ItemID: "b1318-sm1", LocationID: "loc_main", Type: "sale", Quantity: -2, ActorID: "system"},
		{ItemID: "b1318-sm1", LocationID: "loc_main", Type: "sale", Quantity: -3, ActorID: "system"}, // same item twice: running aggregate
		{VariantID: "b1318-smv", LocationID: "loc_main", Type: "sale", Quantity: -1, ActorID: "system"},
		{ItemID: "b1318-sm2", LocationID: "loc_main", Type: "receive", Quantity: 4, CostPrice: 120, ActorID: "system"}, // missing inventory row → insert branch
	}
	ids, err := repo.RecordStockMovements(ctx, tx2, ins)
	if err != nil {
		t.Fatalf("batch: %v", err)
	}
	if len(ids) != 4 {
		t.Fatalf("ids: got %v, want 4", ids)
	}
	if err := tx2.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Movement rows landed with the right identities and quantities.
	var count int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM stock_movements WHERE id IN (?,?,?,?)`, ids[0], ids[1], ids[2], ids[3]).Scan(&count); err != nil || count != 4 {
		t.Fatalf("movement rows: got %d err=%v, want 4", count, err)
	}
	var costPrice sql.NullInt64
	if err := d.DB.QueryRow(`SELECT cost_price FROM stock_movements WHERE id=?`, ids[3]).Scan(&costPrice); err != nil {
		t.Fatalf("read cost_price: %v", err)
	}
	if !costPrice.Valid || costPrice.Int64 != 120 {
		t.Fatalf("cost_price: got %v, want 120", costPrice)
	}
	if err := d.DB.QueryRow(`SELECT cost_price FROM stock_movements WHERE id=?`, ids[0]).Scan(&costPrice); err != nil {
		t.Fatalf("read nil cost_price: %v", err)
	}
	if costPrice.Valid {
		t.Fatalf("zero CostPrice must store NULL (nullInt64 semantics), got %v", costPrice)
	}

	// Inventory: same item's two movements both applied (10 - 2 - 3 = 5).
	// Each check uses its own Scan target — reusing one `qty` variable across
	// Scans that can fail independently previously let a later failure's
	// message print an earlier check's stale value (review finding on this
	// card, ut-docs#1318).
	var sm1Qty, smvQty, sm2Qty float64
	if err := d.DB.QueryRow(`SELECT quantity FROM inventory WHERE item_id='b1318-sm1' AND location_id='loc_main'`).Scan(&sm1Qty); err != nil {
		t.Fatalf("sm1 qty: query failed: %v", err)
	}
	if sm1Qty != 5 {
		t.Fatalf("sm1 qty: got %v, want 5", sm1Qty)
	}
	if err := d.DB.QueryRow(`SELECT quantity FROM inventory WHERE variant_id='b1318-smv' AND location_id='loc_main'`).Scan(&smvQty); err != nil {
		t.Fatalf("smv qty: query failed: %v", err)
	}
	if smvQty != 4 {
		t.Fatalf("smv qty: got %v, want 4", smvQty)
	}
	// Missing-row branch: a fresh inventory row was inserted.
	if err := d.DB.QueryRow(`SELECT quantity FROM inventory WHERE item_id='b1318-sm2' AND location_id='loc_main'`).Scan(&sm2Qty); err != nil {
		t.Fatalf("sm2 inserted qty: query failed: %v", err)
	}
	if sm2Qty != 4 {
		t.Fatalf("sm2 inserted qty: got %v, want 4", sm2Qty)
	}

	// One audit row per movement, entity_id = movement id, action = type.
	for i, id := range ids {
		var action string
		if err := d.DB.QueryRow(`SELECT action FROM audit_log WHERE entity_type='inventory' AND entity_id=?`, id).Scan(&action); err != nil {
			t.Fatalf("audit row %d missing: %v", i, err)
		}
		want := "sale"
		if i == 3 {
			want = "receive"
		}
		if action != want {
			t.Fatalf("audit action %d: got %q, want %q", i, action, want)
		}
	}
}
