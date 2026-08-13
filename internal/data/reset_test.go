package data_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
)

// resetTestDB opens a real migrated DB and returns it with seed/count helpers.
func resetTestDB(t *testing.T, name string) (*db.DB, func(q string, args ...any), func(tbl string) int) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	x := func(q string, args ...any) {
		t.Helper()
		if _, err := d.DB.Exec(q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	count := func(tbl string) int {
		t.Helper()
		var c int
		if err := d.DB.QueryRow("SELECT count(*) FROM " + tbl).Scan(&c); err != nil {
			t.Fatalf("count %s: %v", tbl, err)
		}
		return c
	}
	return d, x, count
}

// seedFullSale seeds one of everything reset touches: a completed sale with a
// line, a line modifier, a discount, a payment, an invoice, a linked return
// sale, plus a shift, a held sale and a stock movement tied to the sale line
// (the shape a real checkout produces — stock_movements.sale_line_id is SET,
// which also pins the child-before-parent archive/delete order).
func seedFullSale(t *testing.T, x func(q string, args ...any)) {
	t.Helper()
	x(`INSERT INTO items (id, name, base_price) VALUES ('i1','Widget',100)`)
	x(`INSERT INTO registers (id, name) VALUES ('r1','Front Till')`)
	x(`INSERT INTO users (id, username, display_name, role) VALUES ('u1','cashier1','Cashier One','cashier')`)
	x(`INSERT INTO sales (id, receipt_no, subtotal, total) VALUES ('s1','R1',100,100)`)
	x(`INSERT INTO sale_lines (id, sale_id, line_no, name_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax, item_id)
	   VALUES ('l1','s1',1,'Widget',1,100,0,0,100,100,'i1')`)
	x(`INSERT INTO sale_line_modifiers (id, sale_line_id, group_name_snapshot, option_name_snapshot, price_delta_minor)
	   VALUES ('slm1','l1','Extras','Extra shot',50)`)
	x(`INSERT INTO sale_discounts (id, sale_id, line_id, type, value, amount, reason) VALUES ('d1','s1','l1','fixed',10,10,'test')`)
	x(`INSERT INTO payments (id, sale_id, method_id, amount) VALUES ('p1','s1','cash',100)`)
	x(`INSERT INTO invoices (id, series, invoice_no, display_no, sale_id, customer_name, seller_json, net_total, tax_total, gross_total, vat_breakdown_json, issued_at, issued_by)
	   VALUES ('inv1','A',1,'A-1','s1','Cust','{}',100,0,100,'[]','2026-01-01T00:00:00Z','u1')`)
	x(`INSERT INTO sales (id, receipt_no, subtotal, total, sale_type) VALUES ('s2','R2',-100,-100,'return')`)
	x(`INSERT INTO sale_links (id, sale_id, original_sale_id, reason) VALUES ('sl1','s2','s1','return')`)
	x(`INSERT INTO shifts (id, register_id, cashier_id, opening_cash) VALUES ('sh1','r1','u1',5000)`)
	x(`INSERT INTO held_sales (id, label, total_minor, line_count, payload) VALUES ('h1','table 4',200,1,'{}')`)
	x(`INSERT INTO stock_movements (id, item_id, location_id, sale_line_id, type, quantity) VALUES ('sm1','i1','loc_main','l1','sale',-1)`)
}

// Every live table reset touches (children before parents, matching the
// archive/delete order) paired with its archive counterpart.
var resetTables = []string{
	"invoices", "sale_links", "payments", "sale_discounts",
	"stock_movements", "sale_line_modifiers", "sale_lines", "sales",
	"held_sales", "shifts",
}

func TestResetTransactionHistoryClearsSalesKeepsCatalog(t *testing.T) {
	d, x, count := resetTestDB(t, "reset.db")
	seedFullSale(t, x)
	// ADR-0040 §9: report_archive is a retained legal record, not
	// transactional/test data -- a reset must NOT touch it.
	x(`INSERT INTO report_archive (id, kind, period, content_json) VALUES ('ra1','eod','2026-01-01','{"net":100}')`)

	itemsBefore := count("items") // fresh DB may seed a sample catalog

	n, batchID, err := data.NewPOSRepo(d.DB).ResetTransactionHistory(context.Background(), "")
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if n != 2 {
		t.Fatalf("sales archived = %d, want 2", n)
	}
	if batchID == "" {
		t.Fatal("reset must return the archive batch id")
	}
	for _, tbl := range resetTables {
		if c := count(tbl); c != 0 {
			t.Fatalf("%s not cleared: %d", tbl, c)
		}
	}
	// ADR-0042: nothing is destroyed — every row moved into its archive
	// table, tagged with the batch id.
	for _, tbl := range resetTables {
		var c int
		if err := d.DB.QueryRow("SELECT count(*) FROM "+tbl+"_archive WHERE reset_batch_id = ?", batchID).Scan(&c); err != nil {
			t.Fatalf("count %s_archive: %v", tbl, err)
		}
		if c != 1 && tbl != "sales" {
			t.Fatalf("%s_archive: want 1 archived row for batch, got %d", tbl, c)
		}
		if tbl == "sales" && c != 2 {
			t.Fatalf("sales_archive: want 2 archived rows for batch, got %d", c)
		}
	}
	var batchCount, salesCount int
	if err := d.DB.QueryRow(`SELECT count(*), COALESCE(MAX(sales_count),0) FROM reset_batches WHERE id = ?`, batchID).Scan(&batchCount, &salesCount); err != nil {
		t.Fatalf("reset_batches: %v", err)
	}
	if batchCount != 1 || salesCount != 2 {
		t.Fatalf("reset_batches row: count=%d sales_count=%d, want 1/2", batchCount, salesCount)
	}
	if c := count("items"); c != itemsBefore {
		t.Fatalf("catalog must survive, items %d -> %d", itemsBefore, c)
	}
	// ADR-0040 §9: report_archive is a retained legal record and must
	// survive a transaction-history reset regardless of retention mode.
	if c := count("report_archive"); c != 1 {
		t.Fatalf("report_archive must survive a reset (retained legal record, ADR-0040 §9), got %d row(s)", c)
	}
	var action string
	if err := d.DB.QueryRow(`SELECT action FROM audit_log WHERE action='transaction_history_reset'`).Scan(&action); err != nil {
		t.Fatalf("reset not audited: %v", err)
	}
}

func TestResetThenRestoreRoundTrip(t *testing.T) {
	d, x, count := resetTestDB(t, "restore.db")
	seedFullSale(t, x)

	repo := data.NewPOSRepo(d.DB)
	ctx := context.Background()
	n, batchID, err := repo.ResetTransactionHistory(ctx, "")
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if n != 2 {
		t.Fatalf("sales archived = %d, want 2", n)
	}

	batches, err := repo.ListResetBatches(ctx)
	if err != nil {
		t.Fatalf("list batches: %v", err)
	}
	if len(batches) != 1 || batches[0].ID != batchID || batches[0].SalesCount != 2 {
		t.Fatalf("ListResetBatches = %+v, want one batch %s with 2 sales", batches, batchID)
	}

	restored, err := repo.RestoreResetBatch(ctx, batchID, "")
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if restored != 2 {
		t.Fatalf("sales restored = %d, want 2", restored)
	}
	// Every live table has its original row(s) back...
	wantLive := map[string]int{
		"sales": 2, "sale_lines": 1, "sale_line_modifiers": 1,
		"sale_discounts": 1, "sale_links": 1, "payments": 1, "invoices": 1,
		"held_sales": 1, "shifts": 1, "stock_movements": 1,
	}
	for tbl, want := range wantLive {
		if c := count(tbl); c != want {
			t.Fatalf("%s after restore: %d rows, want %d", tbl, c, want)
		}
	}
	// ...with matching data (spot-check a value per representative table).
	var total int64
	if err := d.DB.QueryRow(`SELECT total FROM sales WHERE id='s1'`).Scan(&total); err != nil || total != 100 {
		t.Fatalf("restored sale s1: total=%d err=%v, want 100", total, err)
	}
	var receiptNo string
	if err := d.DB.QueryRow(`SELECT receipt_no FROM sales WHERE id='s2'`).Scan(&receiptNo); err != nil || receiptNo != "R2" {
		t.Fatalf("restored sale s2: receipt_no=%q err=%v, want R2", receiptNo, err)
	}
	var optName string
	if err := d.DB.QueryRow(`SELECT option_name_snapshot FROM sale_line_modifiers WHERE id='slm1'`).Scan(&optName); err != nil || optName != "Extra shot" {
		t.Fatalf("restored modifier: %q err=%v, want Extra shot", optName, err)
	}
	var payload string
	if err := d.DB.QueryRow(`SELECT payload FROM held_sales WHERE id='h1'`).Scan(&payload); err != nil || payload != "{}" {
		t.Fatalf("restored held sale: payload=%q err=%v", payload, err)
	}
	var openingCash int64
	if err := d.DB.QueryRow(`SELECT opening_cash FROM shifts WHERE id='sh1'`).Scan(&openingCash); err != nil || openingCash != 5000 {
		t.Fatalf("restored shift: opening_cash=%d err=%v, want 5000", openingCash, err)
	}
	var smLine string
	if err := d.DB.QueryRow(`SELECT sale_line_id FROM stock_movements WHERE id='sm1'`).Scan(&smLine); err != nil || smLine != "l1" {
		t.Fatalf("restored stock movement: sale_line_id=%q err=%v, want l1", smLine, err)
	}
	// The archive is emptied for this batch, and the batch stops existing
	// (ADR-0042 §2: a restored batch cannot be restored twice).
	for _, tbl := range resetTables {
		var c int
		if err := d.DB.QueryRow("SELECT count(*) FROM "+tbl+"_archive WHERE reset_batch_id = ?", batchID).Scan(&c); err != nil {
			t.Fatalf("count %s_archive: %v", tbl, err)
		}
		if c != 0 {
			t.Fatalf("%s_archive still holds %d row(s) after restore", tbl, c)
		}
	}
	if c := count("reset_batches"); c != 0 {
		t.Fatalf("reset_batches should be empty after restore, got %d", c)
	}
	var action string
	if err := d.DB.QueryRow(`SELECT action FROM audit_log WHERE action='transaction_history_restored'`).Scan(&action); err != nil {
		t.Fatalf("restore not audited: %v", err)
	}
}

// Independent review, ut-docs#187: seedFullSale's one invoice has no credit
// note, so the invoices self-FK two-phase archive/restore ordering
// (original_invoice_id) was previously exercised by neither existing test —
// a real gap, since credit notes are a live, GoBD-relevant document type.
func TestResetThenRestoreRoundTrip_CreditNote(t *testing.T) {
	d, x, count := resetTestDB(t, "restore-credit-note.db")
	x(`INSERT INTO items (id, name, base_price) VALUES ('i1','Widget',100)`)
	x(`INSERT INTO sales (id, receipt_no, subtotal, total) VALUES ('s1','R1',100,100)`)
	x(`INSERT INTO invoices (id, series, invoice_no, display_no, sale_id, customer_name, seller_json, net_total, tax_total, gross_total, vat_breakdown_json, issued_at, issued_by)
	   VALUES ('inv1','A',1,'A-1','s1','Cust','{}',100,0,100,'[]','2026-01-01T00:00:00Z','u1')`)
	x(`INSERT INTO invoices (id, series, invoice_no, display_no, kind, sale_id, original_invoice_id, customer_name, seller_json, net_total, tax_total, gross_total, vat_breakdown_json, issued_at, issued_by)
	   VALUES ('cn1','A',2,'A-2','credit_note','s1','inv1','Cust','{}',100,0,100,'[]','2026-01-02T00:00:00Z','u1')`)

	repo := data.NewPOSRepo(d.DB)
	ctx := context.Background()
	_, batchID, err := repo.ResetTransactionHistory(ctx, "")
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if c := count("invoices"); c != 0 {
		t.Fatalf("invoices not cleared: %d", c)
	}
	var archived int
	if err := d.DB.QueryRow(`SELECT count(*) FROM invoices_archive WHERE reset_batch_id = ?`, batchID).Scan(&archived); err != nil || archived != 2 {
		t.Fatalf("invoices_archive: got %d err=%v, want 2 (original + credit note)", archived, err)
	}

	if _, err := repo.RestoreResetBatch(ctx, batchID, ""); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if c := count("invoices"); c != 2 {
		t.Fatalf("invoices after restore: %d, want 2", c)
	}
	var kind, orig string
	if err := d.DB.QueryRow(`SELECT kind, original_invoice_id FROM invoices WHERE id='cn1'`).Scan(&kind, &orig); err != nil {
		t.Fatalf("restored credit note: %v", err)
	}
	if kind != "credit_note" || orig != "inv1" {
		t.Fatalf("restored credit note: kind=%q original_invoice_id=%q, want credit_note/inv1", kind, orig)
	}
}

func TestRestoreRefusesWhenShopHasTradedSinceReset(t *testing.T) {
	d, x, count := resetTestDB(t, "traded.db")
	seedFullSale(t, x)

	repo := data.NewPOSRepo(d.DB)
	ctx := context.Background()
	_, batchID, err := repo.ResetTransactionHistory(ctx, "")
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	// The shop trades after the reset: a new sale occupies the live table
	// (and would collide on receipt numbering — ADR-0042 §2).
	x(`INSERT INTO sales (id, receipt_no, subtotal, total) VALUES ('post1','R1',999,999)`)

	_, err = repo.RestoreResetBatch(ctx, batchID, "")
	if !errors.Is(err, data.ErrShopHasTradedSinceReset) {
		t.Fatalf("restore after trading: err=%v, want ErrShopHasTradedSinceReset", err)
	}
	// Refusal must touch NOTHING: the new sale stays, the archive stays.
	if c := count("sales"); c != 1 {
		t.Fatalf("post-reset sale must be untouched, sales=%d", c)
	}
	var archived int
	if err := d.DB.QueryRow(`SELECT count(*) FROM sales_archive WHERE reset_batch_id = ?`, batchID).Scan(&archived); err != nil || archived != 2 {
		t.Fatalf("archived sales must be untouched: %d err=%v, want 2", archived, err)
	}
	if c := count("reset_batches"); c != 1 {
		t.Fatalf("reset_batches must keep the refused batch, got %d", c)
	}
}

// Independent review, ut-docs#187: reset empties the live sale_lines table,
// so CleanupObsoleteItems / "Remove sample data" / a future catalog action
// that decides an item is safe to delete by checking for LIVE sale_lines
// rows can legitimately remove an item an ARCHIVED batch still references —
// the archive is invisible to that check. Restoring afterward must not
// crash with a raw FK error; it must refuse cleanly, roll back, and leave
// the archive intact for a human to sort out (there is no delete-archive
// action yet to lose it to).
func TestRestoreRefusesWhenArchiveReferencesRemovedItem(t *testing.T) {
	d, x, count := resetTestDB(t, "item-removed.db")
	seedFullSale(t, x)

	repo := data.NewPOSRepo(d.DB)
	ctx := context.Background()
	_, batchID, err := repo.ResetTransactionHistory(ctx, "")
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	// Simulate "Remove sample data" / catalog cleanup: with sale_lines now
	// empty, nothing live references item i1 anymore, so it gets deleted —
	// exactly what those actions do today.
	x(`DELETE FROM items WHERE id = 'i1'`)

	_, err = repo.RestoreResetBatch(ctx, batchID, "")
	if !errors.Is(err, data.ErrArchiveReferencesRemoved) {
		t.Fatalf("restore after item removed: err=%v, want ErrArchiveReferencesRemoved", err)
	}
	// Refused, not partially applied: live tables stay empty, the archive
	// and batch header survive untouched for the next attempt (or a human).
	for _, tbl := range resetTables {
		if c := count(tbl); c != 0 {
			t.Fatalf("refused restore must leave %s empty, got %d", tbl, c)
		}
	}
	var archivedSales int
	if err := d.DB.QueryRow(`SELECT count(*) FROM sales_archive WHERE reset_batch_id = ?`, batchID).Scan(&archivedSales); err != nil || archivedSales != 2 {
		t.Fatalf("archive must survive a refused restore: %d err=%v, want 2", archivedSales, err)
	}
	if c := count("reset_batches"); c != 1 {
		t.Fatalf("reset_batches must keep the refused batch, got %d", c)
	}
}

func TestRestoreNonexistentBatchReturnsNotFound(t *testing.T) {
	d, _, _ := resetTestDB(t, "notfound.db")
	_, err := data.NewPOSRepo(d.DB).RestoreResetBatch(context.Background(), "no-such-batch", "")
	if !errors.Is(err, data.ErrResetBatchNotFound) {
		t.Fatalf("restore of unknown batch: err=%v, want ErrResetBatchNotFound", err)
	}
}

func TestEraseCustomer(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "erase.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	x := func(q string, a ...any) {
		if _, err := d.DB.Exec(q, a...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	x(`INSERT INTO customers (id, name, phone, email) VALUES ('c1','Ada Lovelace','555','ada@x.com')`)
	x(`INSERT INTO sales (id, receipt_no, subtotal, total, customer_id) VALUES ('s1','R1',100,100,'c1')`)

	repo := data.NewPOSRepo(d.DB)
	found, err := repo.SearchCustomers(context.Background(), "ada", 10)
	if err != nil || len(found) != 1 || found[0].ID != "c1" {
		t.Fatalf("search: err=%v found=%+v", err, found)
	}
	ok, err := repo.EraseCustomer(context.Background(), "c1", "")
	if err != nil || !ok {
		t.Fatalf("erase: ok=%v err=%v", ok, err)
	}
	var custs int
	d.DB.QueryRow(`SELECT count(*) FROM customers WHERE id='c1'`).Scan(&custs)
	if custs != 0 {
		t.Fatalf("customer not erased")
	}
	// The sale is KEPT but anonymised (customer_id NULL).
	var cid *string
	var saleCount int
	d.DB.QueryRow(`SELECT count(*) FROM sales WHERE id='s1'`).Scan(&saleCount)
	d.DB.QueryRow(`SELECT customer_id FROM sales WHERE id='s1'`).Scan(&cid)
	if saleCount != 1 || cid != nil {
		t.Fatalf("sale should be kept + unlinked: count=%d cid=%v", saleCount, cid)
	}
	var action string
	if err := d.DB.QueryRow(`SELECT action FROM audit_log WHERE action='customer_erased'`).Scan(&action); err != nil {
		t.Fatalf("erasure not audited: %v", err)
	}
}

func TestCleanupObsoleteItems(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "cleanup.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	x := func(q string, a ...any) {
		if _, err := d.DB.Exec(q, a...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	// obs: inactive + never sold -> removable.
	x(`INSERT INTO stock_locations (id, name) VALUES ('loc1','Main')`)
	x(`INSERT INTO items (id, name, base_price, is_active) VALUES ('obs','Old Test Product',100,0)`)
	x(`INSERT INTO inventory (id, item_id, location_id, quantity) VALUES ('inv1','obs','loc1',0)`)
	// sold: inactive BUT has a sale line -> must be kept.
	x(`INSERT INTO items (id, name, base_price, is_active) VALUES ('sold','Discontinued But Sold',100,0)`)
	x(`INSERT INTO sales (id, receipt_no, subtotal, total) VALUES ('s1','R1',100,100)`)
	x(`INSERT INTO sale_lines (id, sale_id, line_no, name_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax, item_id)
	   VALUES ('l1','s1',1,'Discontinued But Sold',1,100,0,0,100,100,'sold')`)
	// active: not a cleanup target.
	x(`INSERT INTO items (id, name, base_price, is_active) VALUES ('live','Current Product',100,1)`)

	repo := data.NewPOSRepo(d.DB)
	preview, err := repo.ListObsoleteItems(context.Background(), 100)
	if err != nil || len(preview) != 1 || preview[0].ID != "obs" {
		t.Fatalf("preview should list only 'obs': err=%v got=%+v", err, preview)
	}
	n, err := repo.CleanupObsoleteItems(context.Background(), "")
	if err != nil || n != 1 {
		t.Fatalf("cleanup: n=%d err=%v", n, err)
	}
	has := func(id string) bool {
		var c int
		d.DB.QueryRow(`SELECT count(*) FROM items WHERE id=?`, id).Scan(&c)
		return c == 1
	}
	if has("obs") {
		t.Fatal("obsolete item not removed")
	}
	if !has("sold") || !has("live") {
		t.Fatal("sold/active items must be kept")
	}
	var action string
	if err := d.DB.QueryRow(`SELECT action FROM audit_log WHERE action='catalog_cleanup'`).Scan(&action); err != nil {
		t.Fatalf("cleanup not audited: %v", err)
	}
}

// An obsolete item carrying a kitchen-station override must still clean up
// (code review, ut-docs#516): item_station_routes has no pre-delete step in
// CleanupObsoleteItems, so this only works because 034_kitchen_stations.sql
// gives both routing tables ON DELETE CASCADE — without it this reproduces
// a raw "FOREIGN KEY constraint failed" for the whole batch.
func TestCleanupObsoleteItems_ItemWithKitchenStationRouteCascades(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "cleanup-station.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	x := func(q string, a ...any) {
		if _, err := d.DB.Exec(q, a...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	x(`INSERT INTO items (id, name, base_price, is_active) VALUES ('obs2','Old Grill Item',100,0)`)

	repo := data.NewPOSRepo(d.DB)
	stationID, err := repo.CreateKitchenStation(context.Background(), "Grill", "g:9100")
	if err != nil {
		t.Fatalf("CreateKitchenStation: %v", err)
	}
	if err := repo.SetItemStationRoutes(context.Background(), "obs2", []string{stationID}); err != nil {
		t.Fatalf("SetItemStationRoutes: %v", err)
	}

	n, err := repo.CleanupObsoleteItems(context.Background(), "")
	if err != nil {
		t.Fatalf("cleanup must not fail on an item with a station route: %v", err)
	}
	if n != 1 {
		t.Fatalf("want 1 item cleaned up, got %d", n)
	}
	var routeCount int
	d.DB.QueryRow(`SELECT count(*) FROM item_station_routes WHERE item_id='obs2'`).Scan(&routeCount)
	if routeCount != 0 {
		t.Fatalf("station route must cascade-delete with its item, got %d rows left", routeCount)
	}
}
