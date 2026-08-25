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
	// ADR-0062/ut-docs#984: sale_charges has no ON DELETE CASCADE from
	// sales, so it must archive/clear before sales does — this row is what
	// pins that ordering in the reset/restore round-trip tests below.
	x(`INSERT INTO sale_charges (sale_id, seq, key, label, amount_minor, tax_basis_bp, base) VALUES ('s1',0,'service_charge','',10,0,'net_lines')`)
	x(`INSERT INTO payments (id, sale_id, method_id, amount) VALUES ('p1','s1','cash',100)`)
	x(`INSERT INTO invoices (id, series, invoice_no, display_no, sale_id, customer_name, seller_json, net_total, tax_total, gross_total, vat_breakdown_json, issued_at, issued_by)
	   VALUES ('inv1','A',1,'A-1','s1','Cust','{}',100,0,100,'[]','2026-01-01T00:00:00Z','u1')`)
	x(`INSERT INTO sales (id, receipt_no, subtotal, total, sale_type) VALUES ('s2','R2',-100,-100,'return')`)
	x(`INSERT INTO sale_links (id, sale_id, original_sale_id, reason) VALUES ('sl1','s2','s1','return')`)
	x(`INSERT INTO shifts (id, register_id, cashier_id, opening_cash) VALUES ('sh1','r1','u1',5000)`)
	// ut-docs#814/ADR-0054: held_sales.table_id (054_tables.sql) — seeded here
	// so its round-trip through held_sales_archive (055_held_sales_archive_
	// table_id.sql) is pinned by the same reset/restore tests as every other
	// column, not left to the reviewer's manual check alone.
	x(`INSERT INTO tables (id, label, area_zone, seat_count, shape, pos_x, pos_y, created_at, updated_at) VALUES ('tbl1','T1','Terrace',4,'rect',500,500,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`)
	x(`INSERT INTO held_sales (id, label, total_minor, line_count, payload, table_id) VALUES ('h1','table 4',200,1,'{}','tbl1')`)
	x(`INSERT INTO stock_movements (id, item_id, location_id, sale_line_id, type, quantity) VALUES ('sm1','i1','loc_main','l1','sale',-1)`)
}

// Every live table reset touches (children before parents, matching the
// archive/delete order) paired with its archive counterpart.
var resetTables = []string{
	"invoices", "sale_links", "payments", "sale_discounts",
	"stock_movements", "sale_line_modifiers", "sale_lines", "sale_charges", "sales",
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
		"held_sales": 1, "shifts": 1, "stock_movements": 1, "sale_charges": 1,
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
	// ut-docs#814/ADR-0054, 055_held_sales_archive_table_id.sql: table_id
	// must survive the archive round-trip like every other held_sales column.
	var tableID string
	if err := d.DB.QueryRow(`SELECT table_id FROM held_sales WHERE id='h1'`).Scan(&tableID); err != nil || tableID != "tbl1" {
		t.Fatalf("restored held sale: table_id=%q err=%v, want tbl1", tableID, err)
	}
	var openingCash int64
	if err := d.DB.QueryRow(`SELECT opening_cash FROM shifts WHERE id='sh1'`).Scan(&openingCash); err != nil || openingCash != 5000 {
		t.Fatalf("restored shift: opening_cash=%d err=%v, want 5000", openingCash, err)
	}
	var smLine string
	if err := d.DB.QueryRow(`SELECT sale_line_id FROM stock_movements WHERE id='sm1'`).Scan(&smLine); err != nil || smLine != "l1" {
		t.Fatalf("restored stock movement: sale_line_id=%q err=%v, want l1", smLine, err)
	}
	var chargeKey string
	var chargeAmount int64
	if err := d.DB.QueryRow(`SELECT key, amount_minor FROM sale_charges WHERE sale_id='s1' AND seq=0`).Scan(&chargeKey, &chargeAmount); err != nil || chargeKey != "service_charge" || chargeAmount != 10 {
		t.Fatalf("restored sale charge: key=%q amount=%d err=%v, want service_charge/10", chargeKey, chargeAmount, err)
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

// ut-docs#543: card-present reconciliation fields (masked PAN, auth code,
// terminal/trace ID) are exactly the kind of ALTER TABLE payments column
// this migration's own header comment (040_reset_archive.sql) says must be
// mirrored onto payments_archive -- confirm they actually round-trip
// through a reset + restore, not just persist on the live table.
func TestResetThenRestoreRoundTrip_CardPresentFields(t *testing.T) {
	d, x, count := resetTestDB(t, "restore_card_present.db")
	seedFullSale(t, x)
	if _, err := d.DB.Exec(`UPDATE payments SET masked_pan=?, auth_code=?, terminal_id=?, trace_id=? WHERE id='p1'`,
		"VISA •••• 4242", "013579", "TERM-01", "TRACE-99"); err != nil {
		t.Fatalf("seed card-present fields: %v", err)
	}

	repo := data.NewPOSRepo(d.DB)
	ctx := context.Background()
	if _, batchID, err := repo.ResetTransactionHistory(ctx, ""); err != nil {
		t.Fatalf("reset: %v", err)
	} else {
		var maskedPAN string
		if err := d.DB.QueryRow(`SELECT masked_pan FROM payments_archive WHERE id='p1' AND reset_batch_id=?`, batchID).Scan(&maskedPAN); err != nil {
			t.Fatalf("read archived masked_pan: %v", err)
		}
		if maskedPAN != "VISA •••• 4242" {
			t.Fatalf("archived masked_pan = %q, want %q", maskedPAN, "VISA •••• 4242")
		}

		if _, err := repo.RestoreResetBatch(ctx, batchID, ""); err != nil {
			t.Fatalf("restore: %v", err)
		}
	}
	if c := count("payments"); c != 1 {
		t.Fatalf("payments after restore: %d rows, want 1", c)
	}
	var maskedPAN, authCode, terminalID, traceID string
	if err := d.DB.QueryRow(`SELECT masked_pan, auth_code, terminal_id, trace_id FROM payments WHERE id='p1'`).
		Scan(&maskedPAN, &authCode, &terminalID, &traceID); err != nil {
		t.Fatalf("read restored payment: %v", err)
	}
	if maskedPAN != "VISA •••• 4242" || authCode != "013579" || terminalID != "TERM-01" || traceID != "TRACE-99" {
		t.Fatalf("restored card-present fields = %q %q %q %q, want originals back", maskedPAN, authCode, terminalID, traceID)
	}
}

// ut-docs#820 (ADR-0054 follow-on): sales.table_id (056_sale_table_id.sql)
// must round-trip through sales_archive exactly like every earlier ALTER
// this migration's own 040_reset_archive.sql header documents — the same
// reviewer finding that caught held_sales_archive missing table_id
// (055_held_sales_archive_table_id.sql) applies here, so this is pinned
// directly rather than left to a manual column-list check.
func TestResetThenRestoreRoundTrip_SaleTableID(t *testing.T) {
	d, x, count := resetTestDB(t, "restore_sale_table_id.db")
	seedFullSale(t, x)
	if _, err := d.DB.Exec(`UPDATE sales SET table_id=? WHERE id='s1'`, "tbl1"); err != nil {
		t.Fatalf("seed sale table_id: %v", err)
	}

	repo := data.NewPOSRepo(d.DB)
	ctx := context.Background()
	_, batchID, err := repo.ResetTransactionHistory(ctx, "")
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	var archivedTableID string
	if err := d.DB.QueryRow(`SELECT table_id FROM sales_archive WHERE id='s1' AND reset_batch_id=?`, batchID).Scan(&archivedTableID); err != nil {
		t.Fatalf("read archived table_id: %v", err)
	}
	if archivedTableID != "tbl1" {
		t.Fatalf("archived table_id = %q, want tbl1", archivedTableID)
	}

	if _, err := repo.RestoreResetBatch(ctx, batchID, ""); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if c := count("sales"); c != 2 {
		t.Fatalf("sales after restore: %d rows, want 2", c)
	}
	var restoredTableID string
	if err := d.DB.QueryRow(`SELECT table_id FROM sales WHERE id='s1'`).Scan(&restoredTableID); err != nil {
		t.Fatalf("read restored table_id: %v", err)
	}
	if restoredTableID != "tbl1" {
		t.Fatalf("restored table_id = %q, want tbl1", restoredTableID)
	}
}

// ut-docs#527 (independent review): sales.tracking_token (058) must
// round-trip through sales_archive for exactly the reason 056 and 055 did —
// 040_reset_archive.sql's own header says the archive is column-identical to
// the live table across every later ALTER, and ResetTransactionHistory copies
// an EXPLICIT column list, so a missed mirror silently drops the column
// instead of failing loudly. Concretely: a shop that clears transaction
// history and restores the batch would get its sales back with dead tracking
// QRs, contradicting ADR-0042's "destroys nothing". Pinned here rather than
// left to a manual column-list check.
func TestResetThenRestoreRoundTrip_SaleTrackingToken(t *testing.T) {
	d, x, count := resetTestDB(t, "restore_sale_tracking_token.db")
	seedFullSale(t, x)

	repo := data.NewPOSRepo(d.DB)
	ctx := context.Background()
	token, err := repo.EnsureOrderTrackingToken(ctx, "R1")
	if err != nil {
		t.Fatalf("EnsureOrderTrackingToken: %v", err)
	}

	_, batchID, err := repo.ResetTransactionHistory(ctx, "")
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	var archivedToken string
	if err := d.DB.QueryRow(`SELECT COALESCE(tracking_token,'') FROM sales_archive WHERE id='s1' AND reset_batch_id=?`, batchID).Scan(&archivedToken); err != nil {
		t.Fatalf("read archived tracking_token: %v", err)
	}
	if archivedToken != token {
		t.Fatalf("archived tracking_token = %q, want %q", archivedToken, token)
	}

	if _, err := repo.RestoreResetBatch(ctx, batchID, ""); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if c := count("sales"); c != 2 {
		t.Fatalf("sales after restore: %d rows, want 2", c)
	}
	// Restored, and still resolvable by the token the customer's QR carries.
	o, found, err := repo.LookupOrderByTrackingToken(ctx, token)
	if err != nil {
		t.Fatalf("LookupOrderByTrackingToken after restore: %v", err)
	}
	if !found || o.ReceiptNo != "R1" {
		t.Fatalf("restored sale must still resolve by its tracking token: found=%v %+v", found, o)
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

// ut-docs#640: right after a reset-transactions run, the live sale_lines/
// stock_movements tables are empty — the real references to item 'i1' now
// sit in sale_lines_archive/stock_movements_archive instead. Deactivating
// 'i1' afterward must NOT make it look "never sold" to obsoleteItemsWhere;
// both the preview and the delete must keep it, exactly as they would if
// the reference were still live.
func TestCleanupObsoleteItems_KeepsItemReferencedOnlyByArchive(t *testing.T) {
	d, x, _ := resetTestDB(t, "cleanup-archive.db")
	seedFullSale(t, x) // archives item 'i1' via a sale_line AND a stock_movement

	repo := data.NewPOSRepo(d.DB)
	ctx := context.Background()
	if _, _, err := repo.ResetTransactionHistory(ctx, ""); err != nil {
		t.Fatalf("reset: %v", err)
	}
	// Live sale_lines/stock_movements are now empty; only the archive
	// references i1. Deactivate it so it would otherwise match
	// obsoleteItemsWhere.
	x(`UPDATE items SET is_active = 0 WHERE id = 'i1'`)

	preview, err := repo.ListObsoleteItems(ctx, 100)
	if err != nil {
		t.Fatalf("ListObsoleteItems: %v", err)
	}
	for _, it := range preview {
		if it.ID == "i1" {
			t.Fatal("item referenced only by an archived sale_line/stock_movement must not be previewed as obsolete")
		}
	}

	n, err := repo.CleanupObsoleteItems(ctx, "")
	if err != nil {
		t.Fatalf("CleanupObsoleteItems: %v", err)
	}
	if n != 0 {
		t.Fatalf("cleanup removed %d item(s), want 0 — 'i1' is archive-referenced", n)
	}
	var c int
	if err := d.DB.QueryRow(`SELECT count(*) FROM items WHERE id = 'i1'`).Scan(&c); err != nil {
		t.Fatal(err)
	}
	if c != 1 {
		t.Fatal("item referenced only by an archived sale_line/stock_movement was removed")
	}
}

// ut-docs#640 (independent review — an earlier version of this fix refused
// the erasure instead; rejected because DeleteResetBatch's 10-year default
// retention plus RestoreResetBatch's "till has traded since" refusal
// together make "restore or purge it first" almost always impossible,
// turning a GDPR Article 17 request into a multi-year dead end): EraseCustomer
// only used to NULL the LIVE sales.customer_id. Once a customer's only sale
// has gone through reset-transactions, the reference lives in
// sales_archive.customer_id instead, invisible to that UPDATE — erasing the
// customer without also anonymising the archived row would leave it
// pointing at a now-deleted customer, breaking a future RestoreResetBatch.
// Erasure must anonymise BOTH copies and still succeed.
func TestEraseCustomer_AnonymisesArchivedSaleToo(t *testing.T) {
	d, x, _ := resetTestDB(t, "erase-archive.db")
	x(`INSERT INTO customers (id, name, phone, email) VALUES ('c1','Ada Lovelace','555','ada@x.com')`)
	x(`INSERT INTO sales (id, receipt_no, subtotal, total, customer_id) VALUES ('s1','R1',100,100,'c1')`)

	repo := data.NewPOSRepo(d.DB)
	ctx := context.Background()
	if _, _, err := repo.ResetTransactionHistory(ctx, ""); err != nil {
		t.Fatalf("reset: %v", err)
	}
	var archived int
	if err := d.DB.QueryRow(`SELECT count(*) FROM sales_archive WHERE customer_id = 'c1'`).Scan(&archived); err != nil || archived != 1 {
		t.Fatalf("setup: sales_archive.customer_id = c1, count=%d err=%v, want 1", archived, err)
	}

	ok, err := repo.EraseCustomer(ctx, "c1", "")
	if err != nil || !ok {
		t.Fatalf("erase: ok=%v err=%v, want ok=true err=nil", ok, err)
	}
	var custs int
	if err := d.DB.QueryRow(`SELECT count(*) FROM customers WHERE id = 'c1'`).Scan(&custs); err != nil {
		t.Fatal(err)
	}
	if custs != 0 {
		t.Fatal("customer not erased")
	}
	// The archived sale is KEPT (still 1 row) but anonymised.
	var archivedCount int
	var cid *string
	if err := d.DB.QueryRow(`SELECT count(*), customer_id FROM sales_archive WHERE id = 's1'`).Scan(&archivedCount, &cid); err != nil {
		t.Fatal(err)
	}
	if archivedCount != 1 || cid != nil {
		t.Fatalf("archived sale should be kept + unlinked: count=%d cid=%v", archivedCount, cid)
	}

	// Now restore the batch: it must succeed (no dangling FK to the
	// deleted customer) and the restored live sale must stay anonymous.
	batches, err := repo.ListResetBatches(ctx)
	if err != nil || len(batches) != 1 {
		t.Fatalf("ListResetBatches: %+v, %v", batches, err)
	}
	if _, err := repo.RestoreResetBatch(ctx, batches[0].ID, ""); err != nil {
		t.Fatalf("restore after erasure: %v", err)
	}
	var liveCID *string
	if err := d.DB.QueryRow(`SELECT customer_id FROM sales WHERE id = 's1'`).Scan(&liveCID); err != nil {
		t.Fatal(err)
	}
	if liveCID != nil {
		t.Fatalf("restored sale should stay anonymous, got customer_id=%v", *liveCID)
	}
}
