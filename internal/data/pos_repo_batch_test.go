package data

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
	_ "modernc.org/sqlite"
)

// ut-docs#1318: CompleteSale's per-line writes are batched into multi-row
// statements. These tests cover the new batch repo methods directly; the
// CompleteSale-level integration (aggregation across repeated items, the
// preserved stock-check semantics) lives in internal/pos/sales_batch_test.go.

func openBatchDB(t *testing.T) *db.DB {
	t.Helper()
	dbo, err := db.Open(filepath.Join(t.TempDir(), "batch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dbo.Close() })
	return dbo
}

// seedBatchCatalog creates one location, two items and one variant (with the
// FK parents the real migrated schema requires), plus an inventory row for
// itmA (qty 7) and varC (qty 2.5). itmB deliberately has NO inventory row.
func seedBatchCatalog(t *testing.T, dbo *db.DB) {
	t.Helper()
	mustExec(t, dbo, `INSERT INTO stock_locations(id, name) VALUES('loc1', 'Main')`)
	// audit_log.actor_id has an FK to users — the movement batch writes it.
	mustExec(t, dbo, `INSERT INTO users(id, username, display_name, role) VALUES('u1', 'u1', 'Task Runner', 'cashier')`)
	mustExec(t, dbo, `INSERT INTO items(id, sku, name, base_price) VALUES('itmA', 'A', 'Item A', 100)`)
	mustExec(t, dbo, `INSERT INTO items(id, sku, name, base_price) VALUES('itmB', 'B', 'Item B', 200)`)
	mustExec(t, dbo, `INSERT INTO item_variants(id, item_id, name, price) VALUES('varC', 'itmA', 'Large', 150)`)
	mustExec(t, dbo, `INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('invA', 'itmA', NULL, 'loc1', 7, datetime('now'))`)
	mustExec(t, dbo, `INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('invC', NULL, 'varC', 'loc1', 2.5, datetime('now'))`)
}

func TestPOSRepo_CurrentQtyBatch(t *testing.T) {
	dbo := openBatchDB(t)
	seedBatchCatalog(t, dbo)
	ctx := context.Background()
	repo := NewPOSRepo(dbo.DB)

	keyA := StockKey{LocationID: "loc1", ItemID: "itmA"}
	keyB := StockKey{LocationID: "loc1", ItemID: "itmB"}
	keyC := StockKey{LocationID: "loc1", VariantID: "varC"}

	// Duplicate keys in the input must be tolerated (a basket with the same
	// item on two lines produces the same key twice).
	got, err := repo.CurrentQtyBatch(ctx, nil, []StockKey{keyA, keyC, keyB, keyA})
	if err != nil {
		t.Fatalf("CurrentQtyBatch: %v", err)
	}
	if q := got[keyA]; q != 7 {
		t.Errorf("itmA qty = %v, want 7", q)
	}
	if q := got[keyC]; q != 2.5 {
		t.Errorf("varC qty = %v, want 2.5", q)
	}
	if _, ok := got[keyB]; ok {
		t.Errorf("itmB has no inventory row and must be ABSENT from the map, got %v", got[keyB])
	}

	// Parity with the single-row method for each key.
	for _, k := range []StockKey{keyA, keyC} {
		single, found, err := repo.CurrentQty(ctx, nil, k.LocationID, k.ItemID, k.VariantID)
		if err != nil || !found {
			t.Fatalf("CurrentQty(%+v): found=%v err=%v", k, found, err)
		}
		if single != got[k] {
			t.Errorf("CurrentQty(%+v)=%v disagrees with CurrentQtyBatch=%v", k, single, got[k])
		}
	}

	// Empty input: no query, empty map, no error.
	empty, err := repo.CurrentQtyBatch(ctx, nil, nil)
	if err != nil {
		t.Fatalf("CurrentQtyBatch(nil): %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("CurrentQtyBatch(nil) = %v, want empty", empty)
	}
}

func TestPOSRepo_InsertSaleLinesBatch_ChunksAndRoundTrips(t *testing.T) {
	dbo := openBatchDB(t)
	seedBatchCatalog(t, dbo)
	ctx := context.Background()
	repo := NewPOSRepo(dbo.DB)

	mustExec(t, dbo, `INSERT INTO sales(id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at)
VALUES('sale1', 'R1', 'completed', 'sale', 'GBP', 0, 0, 0, 0, datetime('now'))`)

	// 120 rows x 15 columns = 1800 bound parameters — forces multiple chunks
	// under the ~800-parameters-per-statement cap.
	const n = 120
	rows := make([]SaleLineRow, 0, n)
	for i := 0; i < n; i++ {
		rows = append(rows, SaleLineRow{
			ID:             lineIDForTest(i),
			SaleID:         "sale1",
			LineNo:         i + 1,
			ItemID:         "itmA",
			Name:           "Item A",
			SKU:            "A",
			Qty:            2,
			UnitPrice:      100,
			TaxRateBP:      2000,
			TaxAmount:      40,
			TotalBeforeTax: 200,
			TotalAfterTax:  240,
		})
	}

	tx, err := dbo.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.InsertSaleLinesBatch(ctx, tx, rows); err != nil {
		tx.Rollback()
		t.Fatalf("InsertSaleLinesBatch: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	var count int
	if err := dbo.DB.QueryRow(`SELECT COUNT(*) FROM sale_lines WHERE sale_id='sale1'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != n {
		t.Fatalf("sale_lines count = %d, want %d", count, n)
	}
	// Spot-check one row's fields survived the multi-row VALUES packing.
	var lineNo int
	var name string
	var qty float64
	var afterTax int64
	if err := dbo.DB.QueryRow(`SELECT line_no, name_snapshot, quantity, total_after_tax FROM sale_lines WHERE id = ?`, lineIDForTest(70)).Scan(&lineNo, &name, &qty, &afterTax); err != nil {
		t.Fatalf("read back row 70: %v", err)
	}
	if lineNo != 71 || name != "Item A" || qty != 2 || afterTax != 240 {
		t.Fatalf("row 70 round-trip mismatch: line_no=%d name=%q qty=%v after_tax=%d", lineNo, name, qty, afterTax)
	}

	// Empty batch is a no-op, not an error.
	tx2, err := dbo.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx2.Rollback()
	if err := repo.InsertSaleLinesBatch(ctx, tx2, nil); err != nil {
		t.Fatalf("InsertSaleLinesBatch(nil): %v", err)
	}
}

func lineIDForTest(i int) string {
	return "line-" + string(rune('a'+i/26)) + string(rune('a'+i%26))
}

func TestPOSRepo_InsertSaleLineModifiersAndDiscountsBatch(t *testing.T) {
	dbo := openBatchDB(t)
	seedBatchCatalog(t, dbo)
	ctx := context.Background()
	repo := NewPOSRepo(dbo.DB)

	mustExec(t, dbo, `INSERT INTO sales(id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at)
VALUES('sale1', 'R1', 'completed', 'sale', 'GBP', 0, 0, 0, 0, datetime('now'))`)

	tx, err := dbo.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	lines := []SaleLineRow{
		{ID: "l1", SaleID: "sale1", LineNo: 1, ItemID: "itmA", Name: "Item A", Qty: 1, UnitPrice: 100, TaxRateBP: 0, TotalBeforeTax: 100, TotalAfterTax: 100},
		{ID: "l2", SaleID: "sale1", LineNo: 2, ItemID: "itmB", Name: "Item B", Qty: 1, UnitPrice: 200, TaxRateBP: 0, TotalBeforeTax: 200, TotalAfterTax: 200},
	}
	if err := repo.InsertSaleLinesBatch(ctx, tx, lines); err != nil {
		t.Fatalf("InsertSaleLinesBatch: %v", err)
	}
	mods := []SaleLineModifierRow{
		{ID: "m1", SaleLineID: "l1", GroupID: "g1", OptionID: "o1", GroupName: "Size", OptionName: "Large", PriceDeltaMinor: 50},
		// Empty GroupID/OptionID must persist as NULL, matching the
		// single-row InsertSaleLineModifiers' nullableString handling.
		{ID: "m2", SaleLineID: "l2", GroupName: "Milk", OptionName: "Oat", PriceDeltaMinor: 30},
	}
	if err := repo.InsertSaleLineModifiersBatch(ctx, tx, mods); err != nil {
		t.Fatalf("InsertSaleLineModifiersBatch: %v", err)
	}
	discounts := []SaleDiscountRow{
		// Sale-level: LineID empty must persist as NULL.
		{ID: "d1", SaleID: "sale1", Type: "fixed", Value: 100, Amount: 100, Reason: "sale_discount"},
		{ID: "d2", SaleID: "sale1", LineID: "l2", Type: "fixed", Value: 20, Amount: 20, Reason: "line_discount"},
	}
	if err := repo.InsertSaleDiscountsBatch(ctx, tx, discounts); err != nil {
		t.Fatalf("InsertSaleDiscountsBatch: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	var groupID, optionID sql.NullString
	var delta int64
	if err := dbo.DB.QueryRow(`SELECT group_id, option_id, price_delta_minor FROM sale_line_modifiers WHERE id='m2'`).Scan(&groupID, &optionID, &delta); err != nil {
		t.Fatal(err)
	}
	if groupID.Valid || optionID.Valid || delta != 30 {
		t.Fatalf("m2 round-trip: group_id=%v option_id=%v delta=%d, want NULL/NULL/30", groupID, optionID, delta)
	}
	var m1Group string
	if err := dbo.DB.QueryRow(`SELECT group_id FROM sale_line_modifiers WHERE id='m1'`).Scan(&m1Group); err != nil {
		t.Fatal(err)
	}
	if m1Group != "g1" {
		t.Fatalf("m1 group_id = %q, want g1", m1Group)
	}

	var lineID sql.NullString
	if err := dbo.DB.QueryRow(`SELECT line_id FROM sale_discounts WHERE id='d1'`).Scan(&lineID); err != nil {
		t.Fatal(err)
	}
	if lineID.Valid {
		t.Fatalf("d1 (sale-level) line_id = %v, want NULL", lineID)
	}
	if err := dbo.DB.QueryRow(`SELECT line_id FROM sale_discounts WHERE id='d2'`).Scan(&lineID); err != nil {
		t.Fatal(err)
	}
	if !lineID.Valid || lineID.String != "l2" {
		t.Fatalf("d2 line_id = %v, want l2", lineID)
	}

	// Empty batches are no-ops, not errors (the common zero-modifier,
	// zero-discount sale).
	tx2, err := dbo.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx2.Rollback()
	if err := repo.InsertSaleLineModifiersBatch(ctx, tx2, nil); err != nil {
		t.Fatalf("InsertSaleLineModifiersBatch(nil): %v", err)
	}
	if err := repo.InsertSaleDiscountsBatch(ctx, tx2, nil); err != nil {
		t.Fatalf("InsertSaleDiscountsBatch(nil): %v", err)
	}
}

func TestPOSRepo_RecordStockMovementsBatch(t *testing.T) {
	dbo := openBatchDB(t)
	seedBatchCatalog(t, dbo)
	ctx := context.Background()
	repo := NewPOSRepo(dbo.DB)

	tx, err := dbo.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	keys := []StockKey{
		{LocationID: "loc1", ItemID: "itmA"},
		{LocationID: "loc1", ItemID: "itmB"},
	}
	existing, err := repo.CurrentQtyBatch(ctx, tx, keys)
	if err != nil {
		t.Fatalf("CurrentQtyBatch: %v", err)
	}

	ins := []StockMovementInput{
		// itmA twice: the batch must AGGREGATE the delta per key when
		// updating inventory (7 - 2 - 3 = 2)...
		{ItemID: "itmA", LocationID: "loc1", Type: "sale", Quantity: -2, ActorID: "u1", Reason: "sold"},
		{ItemID: "itmA", LocationID: "loc1", Type: "sale", Quantity: -3, ActorID: "u1", Reason: "sold"},
		// ...while itmB has no inventory row and gets one created at its
		// aggregated delta.
		{ItemID: "itmB", LocationID: "loc1", Type: "sale", Quantity: -4, ActorID: "u1", Reason: "sold"},
	}
	ids, err := repo.RecordStockMovementsBatch(ctx, tx, ins, existing)
	if err != nil {
		t.Fatalf("RecordStockMovementsBatch: %v", err)
	}
	if len(ids) != 3 {
		t.Fatalf("got %d movement ids, want 3", len(ids))
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	// One stock_movements row PER INPUT (not per aggregated key).
	var count int
	if err := dbo.DB.QueryRow(`SELECT COUNT(*) FROM stock_movements`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("stock_movements count = %d, want 3", count)
	}
	var qty float64
	if err := dbo.DB.QueryRow(`SELECT quantity FROM stock_movements WHERE id = ?`, ids[1]).Scan(&qty); err != nil {
		t.Fatalf("movement ids[1] not found by returned id: %v", err)
	}
	if qty != -3 {
		t.Fatalf("movement ids[1] quantity = %v, want -3 (ids must be in input order)", qty)
	}

	// Inventory aggregated correctly.
	if err := dbo.DB.QueryRow(`SELECT quantity FROM inventory WHERE item_id='itmA' AND location_id='loc1'`).Scan(&qty); err != nil {
		t.Fatal(err)
	}
	if qty != 2 {
		t.Fatalf("itmA inventory = %v, want 2", qty)
	}
	if err := dbo.DB.QueryRow(`SELECT quantity FROM inventory WHERE item_id='itmB' AND location_id='loc1'`).Scan(&qty); err != nil {
		t.Fatalf("itmB inventory row was not created: %v", err)
	}
	if qty != -4 {
		t.Fatalf("itmB inventory = %v, want -4", qty)
	}

	// One audit row per movement, entity_id = the movement id, same payload
	// shape as the single-row RecordStockMovement.
	if err := dbo.DB.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE entity_type='inventory'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("audit_log inventory rows = %d, want 3", count)
	}
	var payloadJSON string
	if err := dbo.DB.QueryRow(`SELECT data_json FROM audit_log WHERE entity_id = ?`, ids[2]).Scan(&payloadJSON); err != nil {
		t.Fatalf("audit row for ids[2]: %v", err)
	}
	var payload struct {
		Type     string  `json:"type"`
		Quantity float64 `json:"quantity"`
		Reason   string  `json:"reason"`
	}
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		t.Fatalf("audit payload not JSON: %v", err)
	}
	if payload.Type != "sale" || payload.Quantity != -4 || payload.Reason != "sold" {
		t.Fatalf("audit payload = %+v, want {sale -4 sold}", payload)
	}
}

// A nil existing-map must still be safe: the batch probes with the UPDATE and
// only inserts where no row was affected (full RecordStockMovement semantics).
func TestPOSRepo_RecordStockMovementsBatch_NilExistingMap(t *testing.T) {
	dbo := openBatchDB(t)
	seedBatchCatalog(t, dbo)
	ctx := context.Background()
	repo := NewPOSRepo(dbo.DB)

	tx, err := dbo.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	ins := []StockMovementInput{
		{ItemID: "itmA", LocationID: "loc1", Type: "adjust", Quantity: 5, ActorID: "u1"},
		{ItemID: "itmB", LocationID: "loc1", Type: "adjust", Quantity: 1, ActorID: "u1"},
	}
	if _, err := repo.RecordStockMovementsBatch(ctx, tx, ins, nil); err != nil {
		t.Fatalf("RecordStockMovementsBatch(nil map): %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var qty float64
	if err := dbo.DB.QueryRow(`SELECT quantity FROM inventory WHERE item_id='itmA' AND location_id='loc1'`).Scan(&qty); err != nil {
		t.Fatal(err)
	}
	if qty != 12 {
		t.Fatalf("itmA inventory = %v, want 12 (existing row updated, not duplicated)", qty)
	}
	var invRows int
	if err := dbo.DB.QueryRow(`SELECT COUNT(*) FROM inventory WHERE item_id='itmA'`).Scan(&invRows); err != nil {
		t.Fatal(err)
	}
	if invRows != 1 {
		t.Fatalf("itmA inventory rows = %d, want 1", invRows)
	}
	if err := dbo.DB.QueryRow(`SELECT quantity FROM inventory WHERE item_id='itmB' AND location_id='loc1'`).Scan(&qty); err != nil {
		t.Fatalf("itmB inventory row was not created: %v", err)
	}
	if qty != 1 {
		t.Fatalf("itmB inventory = %v, want 1", qty)
	}
}

func TestPOSRepo_RecordStockMovementsBatch_ValidatesLikeSingleRow(t *testing.T) {
	dbo := openBatchDB(t)
	seedBatchCatalog(t, dbo)
	ctx := context.Background()
	repo := NewPOSRepo(dbo.DB)

	tx, err := dbo.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	cases := []StockMovementInput{
		{ItemID: "itmA", Type: "sale", Quantity: -1},                                        // no location
		{LocationID: "loc1", Type: "sale", Quantity: -1},                                    // neither item nor variant
		{ItemID: "itmA", VariantID: "varC", LocationID: "loc1", Type: "sale", Quantity: -1}, // both
		{ItemID: "itmA", LocationID: "loc1", Quantity: -1},                                  // no type
		{ItemID: "itmA", LocationID: "loc1", Type: "sale", Quantity: 0},                     // zero qty
	}
	for i, bad := range cases {
		if _, err := repo.RecordStockMovementsBatch(ctx, tx, []StockMovementInput{bad}, nil); err == nil {
			t.Errorf("case %d (%+v): expected validation error, got nil", i, bad)
		}
	}

	// Empty input is a no-op.
	ids, err := repo.RecordStockMovementsBatch(ctx, tx, nil, nil)
	if err != nil {
		t.Fatalf("RecordStockMovementsBatch(empty): %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("RecordStockMovementsBatch(empty) ids = %v, want none", ids)
	}
}

// The single-row RecordStockMovement retries without cost_price when the
// column is missing (older schemas); the batch must keep that fallback.
func TestPOSRepo_RecordStockMovementsBatch_MissingCostPriceColumnFallback(t *testing.T) {
	raw, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "nocost.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	raw.SetMaxOpenConns(1)
	stmts := []string{
		`CREATE TABLE stock_movements (id TEXT PRIMARY KEY, item_id TEXT, variant_id TEXT, location_id TEXT NOT NULL, sale_line_id TEXT, type TEXT NOT NULL, quantity REAL NOT NULL, created_at TEXT NOT NULL);`,
		`CREATE TABLE inventory (id TEXT PRIMARY KEY, item_id TEXT, variant_id TEXT, location_id TEXT NOT NULL, quantity REAL NOT NULL, updated_at TEXT NOT NULL);`,
		`CREATE TABLE audit_log (id TEXT PRIMARY KEY, actor_id TEXT, entity_type TEXT NOT NULL, entity_id TEXT NOT NULL, action TEXT NOT NULL, data_json TEXT, created_at TEXT NOT NULL);`,
	}
	for _, s := range stmts {
		if _, err := raw.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	repo := NewPOSRepo(raw)
	tx, err := raw.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	ins := []StockMovementInput{
		{ItemID: "itmA", LocationID: "loc1", Type: "sale", Quantity: -2, CostPrice: 123, ActorID: "u1"},
		{ItemID: "itmA", LocationID: "loc1", Type: "sale", Quantity: -1, ActorID: "u1"},
	}
	ids, err := repo.RecordStockMovementsBatch(ctx, tx, ins, map[StockKey]float64{})
	if err != nil {
		tx.Rollback()
		t.Fatalf("RecordStockMovementsBatch on cost_price-less schema: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("got %d ids, want 2", len(ids))
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM stock_movements`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("stock_movements count = %d, want 2 (no double insert from the fallback retry)", count)
	}
	var qty float64
	if err := raw.QueryRow(`SELECT quantity FROM inventory WHERE item_id='itmA'`).Scan(&qty); err != nil {
		t.Fatal(err)
	}
	if qty != -3 {
		t.Fatalf("inventory = %v, want -3", qty)
	}
}
