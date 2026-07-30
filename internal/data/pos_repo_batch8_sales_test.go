package data

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/db"
)

// Coverage batch 8: the POSRepo sale-lifecycle group — receipt sequencing,
// sale/line persistence, status transitions, payment-failure audit, and the
// read-back lookups the journal/refund/invoice pages rely on.

func openBatch8DB(t *testing.T, name string) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// seedBatch8Sale writes a complete sale (header + 2 lines + 1 payment)
// through the same repo writers checkout uses, so every read-back test
// exercises the real InsertSale/InsertSaleLine roundtrip.
func seedBatch8Sale(t *testing.T, d *db.DB, repo *POSRepo, saleID, receiptNo, createdAt string) {
	t.Helper()
	ctx := context.Background()
	mustExec(t, d, `INSERT OR IGNORE INTO items (id, sku, name, base_price, is_active)
VALUES ('itm-b8', 'B8-SKU', 'Batch8 Item', 500, 1)`)
	mustExec(t, d, `INSERT OR IGNORE INTO payment_methods (id, name, type, is_active)
VALUES ('cash', 'Cash', 'cash', 1)`)

	// subtotal 1000, discount 100, tax 180, total 1080 — exact minor units.
	if err := repo.InsertSale(ctx, nil, saleID, receiptNo, "sale", "", "", "", "GBP",
		1000, 100, 180, 1080, "batch8 note", createdAt, "cash", true, "queued", 2, "", ""); err != nil {
		t.Fatalf("InsertSale: %v", err)
	}
	// Insert line 2 first: ListSaleLineSnapshots/GetSaleDetail must order by line_no,
	// not insertion order.
	if err := repo.InsertSaleLine(ctx, nil, saleID+"-l2", saleID, 2, "itm-b8", "",
		"Batch8 Item", "B8-SKU", "5012345678900", 1, 200, 0, 2000, 40, 200, 240); err != nil {
		t.Fatalf("InsertSaleLine l2: %v", err)
	}
	if err := repo.InsertSaleLine(ctx, nil, saleID+"-l1", saleID, 1, "itm-b8", "",
		"Batch8 Item", "B8-SKU", "5012345678900", 2, 400, 100, 2000, 140, 700, 840); err != nil {
		t.Fatalf("InsertSaleLine l1: %v", err)
	}
	if err := repo.InsertPayment(ctx, nil, saleID+"-p1", saleID, "cash",
		1080, "GBP", "ref-b8", 20, 50, createdAt); err != nil {
		t.Fatalf("InsertPayment: %v", err)
	}
}

func TestPOSRepo_LookupUserRole(t *testing.T) {
	d := openBatch8DB(t, "roles.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	mustExec(t, d, `INSERT INTO users (id, username, display_name, role, is_active)
VALUES ('u-mgr', 'mgr', 'Manager', 'manager', 1)`)

	role, ok, err := repo.LookupUserRole(ctx, "u-mgr")
	if err != nil || !ok || role != "manager" {
		t.Fatalf("LookupUserRole(u-mgr) = (%q,%v,%v), want (manager,true,nil)", role, ok, err)
	}
	// Migration 003 seeds the 'system' admin actor — the refund override path
	// (internal/pages/inventory_api.go) depends on its role resolving.
	role, ok, err = repo.LookupUserRole(ctx, "system")
	if err != nil || !ok || role != "admin" {
		t.Fatalf("LookupUserRole(system) = (%q,%v,%v), want (admin,true,nil)", role, ok, err)
	}
	role, ok, err = repo.LookupUserRole(ctx, "nobody")
	if err != nil || ok || role != "" {
		t.Fatalf("LookupUserRole(unknown) = (%q,%v,%v), want (\"\",false,nil)", role, ok, err)
	}
}

func TestPOSRepo_NextReceiptNo_Sequencing(t *testing.T) {
	d := openBatch8DB(t, "receipts.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	// Empty DB, no prefix configured: sequence starts at 1, zero-padded to 9.
	got, err := repo.NextReceiptNo(ctx, nil)
	if err != nil || got != "000000001" {
		t.Fatalf("NextReceiptNo(empty) = (%q,%v), want (000000001,nil)", got, err)
	}

	// Using the issued number advances the sequence — monotonic, no reuse.
	seedBatch8Sale(t, d, repo, "sale-b8-1", got, "2026-07-30T10:00:00Z")
	got, err = repo.NextReceiptNo(ctx, nil)
	if err != nil || got != "000000002" {
		t.Fatalf("NextReceiptNo(after 1 sale) = (%q,%v), want (000000002,nil)", got, err)
	}

	// Non-numeric legacy receipt casts to 0 and must not break the counter.
	mustExec(t, d, `INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, total, created_at)
VALUES ('sale-legacy', 'R-0099', 'completed', 'sale', 'GBP', 100, 100, '2026-07-30T10:01:00Z')`)
	got, err = repo.NextReceiptNo(ctx, nil)
	if err != nil || got != "000000002" {
		t.Fatalf("NextReceiptNo(with legacy receipt) = (%q,%v), want (000000002,nil)", got, err)
	}

	// ADR-0011 synced replica: the till prefix namespaces the counter, and
	// another till's receipts (different prefix) never bleed into this max.
	mustExec(t, d, `INSERT INTO settings (key, value) VALUES ('sync.receipt_prefix', 'T2-')`)
	mustExec(t, d, `INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, total, created_at)
VALUES ('sale-t2', 'T2-000000007', 'completed', 'sale', 'GBP', 100, 100, '2026-07-30T10:02:00Z')`)
	mustExec(t, d, `INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, total, created_at)
VALUES ('sale-t9', 'T9-000000042', 'completed', 'sale', 'GBP', 100, 100, '2026-07-30T10:03:00Z')`)
	got, err = repo.NextReceiptNo(ctx, nil)
	if err != nil || got != "T2-000000008" {
		t.Fatalf("NextReceiptNo(prefixed) = (%q,%v), want (T2-000000008,nil)", got, err)
	}

	// The checkout path calls it inside a transaction (internal/pos/sales.go:75).
	tx, err := d.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	got, err = repo.NextReceiptNo(ctx, tx)
	if err != nil || got != "T2-000000008" {
		t.Fatalf("NextReceiptNo(in tx) = (%q,%v), want (T2-000000008,nil)", got, err)
	}
}

func TestPOSRepo_InsertSale_ReadBackRoundtrip(t *testing.T) {
	d := openBatch8DB(t, "roundtrip.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)
	created := "2026-07-30T09:30:00Z"
	seedBatch8Sale(t, d, repo, "sale-rt", "000000001", created)

	// SaleTotals: exact integer minor units, straight off the header row.
	receipt, subtotal, tax, total, err := repo.SaleTotals(ctx, "sale-rt")
	if err != nil {
		t.Fatalf("SaleTotals: %v", err)
	}
	if receipt != "000000001" || subtotal != 1000 || tax != 180 || total != 1080 {
		t.Fatalf("SaleTotals = (%q,%d,%d,%d), want (000000001,1000,180,1080)", receipt, subtotal, tax, total)
	}

	receipt, ok, err := repo.GetReceiptNo(ctx, "sale-rt")
	if err != nil || !ok || receipt != "000000001" {
		t.Fatalf("GetReceiptNo = (%q,%v,%v), want (000000001,true,nil)", receipt, ok, err)
	}

	id, ok, err := repo.FindSaleIDByReceipt(ctx, "000000001")
	if err != nil || !ok || id != "sale-rt" {
		t.Fatalf("FindSaleIDByReceipt = (%q,%v,%v), want (sale-rt,true,nil)", id, ok, err)
	}

	// InsertSale stamps completed_at = created_at; the refund-window check
	// (internal/pages/pos_api.go:616) parses it as RFC3339.
	ts, ok, err := repo.SaleCompletedAt(ctx, "sale-rt")
	if err != nil || !ok {
		t.Fatalf("SaleCompletedAt: ok=%v err=%v", ok, err)
	}
	want, _ := time.Parse(time.RFC3339, created)
	if !ts.Equal(want) {
		t.Fatalf("SaleCompletedAt = %v, want %v", ts, want)
	}

	// ListSaleLineSnapshots: ordered by line_no (l2 was inserted first), with
	// the snapshot fields the refund flow re-prices from.
	snaps, err := repo.ListSaleLineSnapshots(ctx, "sale-rt")
	if err != nil {
		t.Fatalf("ListSaleLineSnapshots: %v", err)
	}
	if len(snaps) != 2 {
		t.Fatalf("want 2 snapshots, got %d: %+v", len(snaps), snaps)
	}
	l1 := snaps[0]
	if l1.ID != "sale-rt-l1" || l1.ItemID != "itm-b8" || l1.VariantID != "" ||
		l1.Name != "Batch8 Item" || l1.SKU != "B8-SKU" || l1.Barcode != "5012345678900" ||
		l1.Qty != 2 || l1.UnitPrice != 400 || l1.TaxRateBP != 2000 {
		t.Fatalf("snapshot line 1 mismatch: %+v", l1)
	}
	if snaps[1].ID != "sale-rt-l2" || snaps[1].Qty != 1 || snaps[1].UnitPrice != 200 {
		t.Fatalf("snapshot line 2 mismatch: %+v", snaps[1])
	}

	// GetSaleDetailByID (invoice page path) resolves id -> receipt -> full detail.
	detail, ok, err := repo.GetSaleDetailByID(ctx, "sale-rt")
	if err != nil || !ok {
		t.Fatalf("GetSaleDetailByID: ok=%v err=%v", ok, err)
	}
	if detail.ID != "sale-rt" || detail.ReceiptNo != "000000001" || detail.Status != "completed" ||
		detail.SaleType != "sale" || detail.TenderType != "cash" || !detail.Offline ||
		detail.SyncStatus != "queued" || detail.Currency != "GBP" ||
		detail.Subtotal != 1000 || detail.DiscountTotal != 100 || detail.TaxTotal != 180 ||
		detail.Total != 1080 || detail.CreatedAt != created || detail.CashierID != "" {
		t.Fatalf("GetSaleDetailByID header mismatch: %+v", detail)
	}
	if len(detail.Lines) != 2 {
		t.Fatalf("want 2 detail lines, got %d", len(detail.Lines))
	}
	dl := detail.Lines[0]
	if dl.Name != "Batch8 Item" || dl.SKU != "B8-SKU" || dl.ItemID != "itm-b8" ||
		dl.TaxRateBP != 2000 || dl.Qty != 2 || dl.UnitPrice != 400 ||
		dl.LineDiscount != 100 || dl.TaxAmount != 140 || dl.LineTotal != 840 {
		t.Fatalf("detail line 1 mismatch: %+v", dl)
	}
	if len(detail.Payments) != 1 {
		t.Fatalf("want 1 payment, got %d", len(detail.Payments))
	}
	p := detail.Payments[0]
	if p.Method != "cash" || p.Amount != 1080 || p.ChangeGiven != 20 ||
		p.TipAmount != 50 || p.Reference != "ref-b8" || p.PaidAt != created {
		t.Fatalf("detail payment mismatch: %+v", p)
	}

	// Duplicate receipt_no violates the sales UNIQUE constraint — checkout
	// relies on this to make receipt reuse impossible.
	err = repo.InsertSale(ctx, nil, "sale-dup", "000000001", "sale", "", "", "", "GBP",
		1, 0, 0, 1, "", created, "cash", false, "queued", 0, "", "")
	if err == nil {
		t.Fatal("InsertSale with duplicate receipt_no must fail")
	}

	// A line with neither item nor variant violates the schema CHECK.
	err = repo.InsertSaleLine(ctx, nil, "bad-line", "sale-rt", 3, "", "",
		"Ghost", "", "", 1, 100, 0, 0, 0, 100, 100)
	if err == nil {
		t.Fatal("InsertSaleLine with no item_id and no variant_id must fail")
	}
}

func TestPOSRepo_SaleLookups_NotFound(t *testing.T) {
	d := openBatch8DB(t, "notfound.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	if id, ok, err := repo.FindSaleIDByReceipt(ctx, "999999999"); err != nil || ok || id != "" {
		t.Fatalf("FindSaleIDByReceipt(unknown) = (%q,%v,%v)", id, ok, err)
	}
	if rc, ok, err := repo.GetReceiptNo(ctx, "no-sale"); err != nil || ok || rc != "" {
		t.Fatalf("GetReceiptNo(unknown) = (%q,%v,%v)", rc, ok, err)
	}
	if snaps, err := repo.ListSaleLineSnapshots(ctx, "no-sale"); err != nil || len(snaps) != 0 {
		t.Fatalf("ListSaleLineSnapshots(unknown) = (%+v,%v), want empty", snaps, err)
	}
	// SaleTotals surfaces the raw ErrNoRows — callers must treat it as an error.
	if _, _, _, _, err := repo.SaleTotals(ctx, "no-sale"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("SaleTotals(unknown) err = %v, want sql.ErrNoRows", err)
	}
	if _, ok, err := repo.SaleCompletedAt(ctx, "no-sale"); err != nil || ok {
		t.Fatalf("SaleCompletedAt(unknown) = ok=%v err=%v, want (false,nil)", ok, err)
	}
	if _, ok, err := repo.GetSaleDetailByID(ctx, "no-sale"); err != nil || ok {
		t.Fatalf("GetSaleDetailByID(unknown) = ok=%v err=%v, want (false,nil)", ok, err)
	}
	if entries, err := repo.ListRecentSales(ctx, 5); err != nil || len(entries) != 0 {
		t.Fatalf("ListRecentSales(empty db) = (%+v,%v), want empty", entries, err)
	}
}

func TestPOSRepo_SaleCompletedAt_EmptyAndNull(t *testing.T) {
	d := openBatch8DB(t, "completedat.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	// Blank completed_at reads back as "not completed", not an error.
	mustExec(t, d, `INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, total, created_at, completed_at)
VALUES ('sale-blank', '000000010', 'completed', 'sale', 'GBP', 100, 100, '2026-07-30T10:00:00Z', '')`)
	if _, ok, err := repo.SaleCompletedAt(ctx, "sale-blank"); err != nil || ok {
		t.Fatalf("SaleCompletedAt(blank) = ok=%v err=%v, want (false,nil)", ok, err)
	}

	// NULL completed_at reads back as "not completed", same contract as
	// blank — fixed 2026-07-30 (was a scan error into a string dest;
	// pos_api.go:616 swallowed it and proceeded with a zero time).
	mustExec(t, d, `INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, total, created_at, completed_at)
VALUES ('sale-null', '000000011', 'completed', 'sale', 'GBP', 100, 100, '2026-07-30T10:00:00Z', NULL)`)
	if _, ok, err := repo.SaleCompletedAt(ctx, "sale-null"); err != nil || ok {
		t.Fatalf("SaleCompletedAt(NULL) = ok=%v err=%v, want (false,nil)", ok, err)
	}

	// Unparseable timestamp is an explicit error.
	mustExec(t, d, `INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, total, created_at, completed_at)
VALUES ('sale-badts', '000000012', 'completed', 'sale', 'GBP', 100, 100, '2026-07-30T10:00:00Z', 'not-a-time')`)
	if _, ok, err := repo.SaleCompletedAt(ctx, "sale-badts"); err == nil || ok {
		t.Fatalf("SaleCompletedAt(bad ts) = ok=%v err=%v, want parse error", ok, err)
	}
}

func TestPOSRepo_UpdateSaleStatus(t *testing.T) {
	d := openBatch8DB(t, "status.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)
	seedBatch8Sale(t, d, repo, "sale-st", "000000001", "2026-07-30T11:00:00Z")

	readStatus := func() (status string, voidedAt sql.NullString) {
		t.Helper()
		if err := d.DB.QueryRow(`SELECT status, voided_at FROM sales WHERE id = 'sale-st'`).
			Scan(&status, &voidedAt); err != nil {
			t.Fatal(err)
		}
		return
	}

	// Non-void transition: status changes, voided_at stays NULL.
	if err := repo.UpdateSaleStatus(ctx, nil, "sale-st", "refunded"); err != nil {
		t.Fatalf("UpdateSaleStatus(refunded): %v", err)
	}
	status, voidedAt := readStatus()
	if status != "refunded" || voidedAt.Valid {
		t.Fatalf("after refunded: status=%q voided_at=%+v, want (refunded, NULL)", status, voidedAt)
	}

	// Voiding stamps voided_at with a parseable UTC timestamp.
	before := time.Now().UTC().Add(-2 * time.Second)
	if err := repo.UpdateSaleStatus(ctx, nil, "sale-st", "voided"); err != nil {
		t.Fatalf("UpdateSaleStatus(voided): %v", err)
	}
	status, voidedAt = readStatus()
	if status != "voided" || !voidedAt.Valid {
		t.Fatalf("after voided: status=%q voided_at=%+v", status, voidedAt)
	}
	stamp, err := time.Parse(time.RFC3339, voidedAt.String)
	if err != nil {
		t.Fatalf("voided_at %q not RFC3339: %v", voidedAt.String, err)
	}
	if stamp.Before(before) || stamp.After(time.Now().UTC().Add(2*time.Second)) {
		t.Fatalf("voided_at %v not within test window", stamp)
	}

	// Later non-void status keeps the original voided_at (CASE keeps old value).
	if err := repo.UpdateSaleStatus(ctx, nil, "sale-st", "completed"); err != nil {
		t.Fatal(err)
	}
	status, voidedAt2 := readStatus()
	if status != "completed" || voidedAt2.String != voidedAt.String {
		t.Fatalf("voided_at must survive later transitions: %q -> %q", voidedAt.String, voidedAt2.String)
	}

	// Unknown sale id: an explicit error — fixed 2026-07-30 (was a silent
	// no-op with no RowsAffected check, so voids of nonexistent sales
	// "succeeded" and still left an audit row via internal/pos).
	if err := repo.UpdateSaleStatus(ctx, nil, "no-such-sale", "voided"); err == nil {
		t.Fatal("UpdateSaleStatus(unknown) = nil, want a not-found error")
	}

	// Transactional path: a rollback discards the status change.
	tx, err := d.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateSaleStatus(ctx, tx, "sale-st", "parked"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if status, _ = readStatus(); status != "completed" {
		t.Fatalf("rollback must discard status change, got %q", status)
	}
}

func TestPOSRepo_RecordPaymentFailure(t *testing.T) {
	d := openBatch8DB(t, "payfail.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	gotID, err := repo.RecordPaymentFailure(ctx, PaymentFailure{
		SaleID:   "sale-pf",
		ActorID:  "system", // seeded by migration 003; audit_log.actor_id has an FK
		Reason:   "card declined",
		Payments: []any{map[string]any{"method": "card", "amount": 1080}},
		Lines:    []any{map[string]any{"item_id": "itm-b8", "qty": 1}},
		Total:    1080,
		Currency: "GBP",
	})
	if err != nil || gotID != "sale-pf" {
		t.Fatalf("RecordPaymentFailure = (%q,%v), want (sale-pf,nil)", gotID, err)
	}

	var actorID, entityType, action, dataJSON string
	if err := d.DB.QueryRow(`SELECT actor_id, entity_type, action, data_json
FROM audit_log WHERE entity_id = 'sale-pf'`).Scan(&actorID, &entityType, &action, &dataJSON); err != nil {
		t.Fatalf("read audit row: %v", err)
	}
	if actorID != "system" || entityType != "sale" || action != "payment_failed" {
		t.Fatalf("audit row = (%q,%q,%q), want (system,sale,payment_failed)", actorID, entityType, action)
	}
	var payload struct {
		Reason   string `json:"reason"`
		Total    int64  `json:"total"`
		Currency string `json:"currency"`
		Payments []any  `json:"payments"`
		Lines    []any  `json:"lines"`
		TS       string `json:"ts"`
	}
	if err := json.Unmarshal([]byte(dataJSON), &payload); err != nil {
		t.Fatalf("data_json not JSON: %v (%s)", err, dataJSON)
	}
	if payload.Reason != "card declined" || payload.Total != 1080 || payload.Currency != "GBP" ||
		len(payload.Payments) != 1 || len(payload.Lines) != 1 {
		t.Fatalf("audit payload mismatch: %+v", payload)
	}
	if _, err := time.Parse(time.RFC3339, payload.TS); err != nil {
		t.Fatalf("payload ts %q not RFC3339: %v", payload.TS, err)
	}

	// No sale id yet (failure before the sale row exists): a fresh uuid is
	// generated and used as the audit entity id.
	genID, err := repo.RecordPaymentFailure(ctx, PaymentFailure{
		Reason: "terminal offline", Total: 500, Currency: "GBP",
	})
	if err != nil || genID == "" || genID == "sale-pf" {
		t.Fatalf("RecordPaymentFailure(no sale id) = (%q,%v)", genID, err)
	}
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM audit_log
WHERE entity_id = ? AND action = 'payment_failed' AND actor_id IS NULL`, genID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("generated-id audit row count = %d err=%v, want 1", n, err)
	}
}

func TestPOSRepo_ListRecentSales(t *testing.T) {
	d := openBatch8DB(t, "recent.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	for i, ts := range []string{
		"2026-07-30T09:00:00Z", // oldest
		"2026-07-30T09:05:00Z",
		"2026-07-30T09:10:00Z",
		"2026-07-30T09:15:00Z",
		"2026-07-30T09:20:00Z",
		"2026-07-30T09:25:00Z", // newest
	} {
		mustExec(t, d, `INSERT INTO sales (id, receipt_no, status, sale_type, tender_type, sync_status, currency, subtotal, total, created_at)
VALUES (?, ?, 'completed', 'sale', 'cash', 'synced', 'GBP', ?, ?, ?)`,
			// receipt R1..R6, totals 101..106 minor units
			"sale-r"+string(rune('1'+i)), "R"+string(rune('1'+i)), 100+int64(i)+1, 100+int64(i)+1, ts)
	}

	entries, err := repo.ListRecentSales(ctx, 2)
	if err != nil {
		t.Fatalf("ListRecentSales(2): %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("want 2 entries, got %d", len(entries))
	}
	if entries[0].ReceiptNo != "R6" || entries[0].Total != 106 ||
		entries[0].TenderType != "cash" || entries[0].SyncStatus != "synced" ||
		entries[0].CreatedAt != "2026-07-30T09:25:00Z" {
		t.Fatalf("newest entry mismatch: %+v", entries[0])
	}
	if entries[1].ReceiptNo != "R5" || entries[1].Total != 105 {
		t.Fatalf("second entry mismatch: %+v", entries[1])
	}

	// limit <= 0 falls back to 5 (the journal page default).
	entries, err = repo.ListRecentSales(ctx, 0)
	if err != nil || len(entries) != 5 {
		t.Fatalf("ListRecentSales(0) = %d entries err=%v, want 5", len(entries), err)
	}
	if entries[0].ReceiptNo != "R6" || entries[4].ReceiptNo != "R2" {
		t.Fatalf("default-limit ordering mismatch: first=%+v last=%+v", entries[0], entries[4])
	}
}
