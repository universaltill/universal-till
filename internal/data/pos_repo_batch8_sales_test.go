package data

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
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
	if err := repo.InsertSale(ctx, nil, InsertSaleParams{
		SaleID: saleID, ReceiptNo: receiptNo, SaleType: "sale", Currency: "GBP",
		Subtotal: 1000, DiscountTotal: 100, TaxTotal: 180, Total: 1080,
		Note: "batch8 note", CreatedAt: createdAt, TenderType: "cash",
		Offline: true, SyncStatus: "queued", SyncAttempts: 2,
	}); err != nil {
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
		1080, "GBP", "ref-b8", 20, 50, "employee", "", createdAt, CardPresentFields{}); err != nil {
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
		detail.SaleType != "sale" || detail.TenderType != "cash" || detail.OrderType != "" || !detail.Offline ||
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
	err = repo.InsertSale(ctx, nil, InsertSaleParams{
		SaleID: "sale-dup", ReceiptNo: "000000001", SaleType: "sale", Currency: "GBP",
		Subtotal: 1, Total: 1, CreatedAt: created, TenderType: "cash", SyncStatus: "queued",
	})
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

// TestPOSRepo_InsertSale_OrderTypeRoundtrip covers ut-docs#181: order_type
// was tracked in-memory during checkout but discarded at sale completion,
// so a past sale's receipt/journal/kitchen ticket could never show whether
// it was dine-in or takeaway. Confirms it now survives InsertSale ->
// GetSaleDetail intact.
func TestPOSRepo_InsertSale_OrderTypeRoundtrip(t *testing.T) {
	d := openBatch8DB(t, "order-type.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)
	mustExec(t, d, `INSERT OR IGNORE INTO items (id, sku, name, base_price, is_active)
VALUES ('itm-ot', 'OT-SKU', 'Order Type Item', 500, 1)`)

	created := "2026-08-02T09:00:00Z"
	if err := repo.InsertSale(ctx, nil, InsertSaleParams{
		SaleID: "sale-ot", ReceiptNo: "000000099", SaleType: "sale", Currency: "GBP",
		Subtotal: 500, Total: 500, CreatedAt: created, TenderType: "cash",
		OrderType: "takeaway", SyncStatus: "synced",
	}); err != nil {
		t.Fatalf("InsertSale: %v", err)
	}
	if err := repo.InsertSaleLine(ctx, nil, "sale-ot-l1", "sale-ot", 1, "itm-ot", "",
		"Order Type Item", "OT-SKU", "", 1, 500, 0, 0, 0, 500, 500); err != nil {
		t.Fatalf("InsertSaleLine: %v", err)
	}

	detail, ok, err := repo.GetSaleDetail(ctx, "000000099")
	if err != nil || !ok {
		t.Fatalf("GetSaleDetail: ok=%v err=%v", ok, err)
	}
	if detail.OrderType != "takeaway" {
		t.Fatalf("GetSaleDetail.OrderType = %q, want %q", detail.OrderType, "takeaway")
	}

	byID, ok, err := repo.GetSaleDetailByID(ctx, "sale-ot")
	if err != nil || !ok {
		t.Fatalf("GetSaleDetailByID: ok=%v err=%v", ok, err)
	}
	if byID.OrderType != "takeaway" {
		t.Fatalf("GetSaleDetailByID.OrderType = %q, want %q", byID.OrderType, "takeaway")
	}
}

// TestPOSRepo_InsertSale_RejectsMissingRequiredFields covers ut-docs#989: a
// follow-up to the InsertSaleParams struct refactor (ut-docs#976). The old
// positional signature forced every argument to be supplied at every call
// site; a struct literal lets a genuinely-required field default to its Go
// zero value silently if a future caller simply omits it -- and this
// codebase has already established (docs/code-reviews/
// 2026-08-15-sync-journal-currency-createdat-validation-647.md) that
// SQLite's NOT NULL does not catch an empty string. InsertSale must reject
// a call missing any of the confirmed-required fields, naming the field in
// the error, rather than silently writing an empty column.
func TestPOSRepo_InsertSale_RejectsMissingRequiredFields(t *testing.T) {
	d := openBatch8DB(t, "required-fields.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	base := func() InsertSaleParams {
		return InsertSaleParams{
			SaleID: "sale-req", ReceiptNo: "000000200", SaleType: "sale",
			Currency: "GBP", Subtotal: 100, Total: 100,
			CreatedAt: "2026-08-25T09:00:00Z", TenderType: "cash",
			SyncStatus: "synced",
		}
	}

	// Sanity check: the fully-populated base case must succeed -- otherwise
	// the rejection cases below would prove nothing.
	if err := repo.InsertSale(ctx, nil, base()); err != nil {
		t.Fatalf("InsertSale with all required fields set: %v", err)
	}

	tests := []struct {
		field string
		zero  func(p *InsertSaleParams)
	}{
		{"SaleID", func(p *InsertSaleParams) { p.SaleID = "" }},
		{"ReceiptNo", func(p *InsertSaleParams) { p.ReceiptNo = "" }},
		{"SaleType", func(p *InsertSaleParams) { p.SaleType = "" }},
		{"Currency", func(p *InsertSaleParams) { p.Currency = "" }},
		{"CreatedAt", func(p *InsertSaleParams) { p.CreatedAt = "" }},
		{"SyncStatus", func(p *InsertSaleParams) { p.SyncStatus = "" }},
		{"TenderType", func(p *InsertSaleParams) { p.TenderType = "" }},
	}
	for _, tc := range tests {
		t.Run(tc.field, func(t *testing.T) {
			p := base()
			p.SaleID = "sale-req-" + tc.field // keep PKs distinct per subtest
			p.ReceiptNo = "000000201-" + tc.field
			tc.zero(&p)
			err := repo.InsertSale(ctx, nil, p)
			if err == nil {
				t.Fatalf("InsertSale with empty %s: want error, got nil", tc.field)
			}
			if !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("InsertSale with empty %s: error %q does not name the missing field", tc.field, err.Error())
			}
		})
	}

	// Optional fields must still be safely omittable -- the guard must not
	// over-reach into fields the existing test suite deliberately relies on
	// zero-value omission for (RegisterID, CashierID, CustomerID, TableID,
	// Note, OrderType, sync-retry fields, ServiceCharge/TaxBasisBP).
	optional := base()
	optional.SaleID = "sale-req-optional"
	optional.ReceiptNo = "000000202"
	if err := repo.InsertSale(ctx, nil, optional); err != nil {
		t.Fatalf("InsertSale with only required fields set: %v", err)
	}
}

// TestPOSRepo_InsertSale_TableRoundtrip (ut-docs#820): a completed sale's
// assigned table survives to GetSaleDetail/GetSaleDetailByID with its
// LABEL resolved via a join against `tables`, not just the raw id -- the
// receipt/kitchen-ticket rendering path (kitchenTicketFor) reads
// TableLabel directly with no lookup of its own. Same shape as
// TestPOSRepo_InsertSale_OrderTypeRoundtrip above.
func TestPOSRepo_InsertSale_TableRoundtrip(t *testing.T) {
	d := openBatch8DB(t, "table.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)
	mustExec(t, d, `INSERT OR IGNORE INTO items (id, sku, name, base_price, is_active)
VALUES ('itm-tbl', 'TBL-SKU', 'Table Item', 500, 1)`)

	tableID, err := repo.CreateTable(ctx, "T7", "Terrace", 4, "rect", 200, 300)
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}

	created := "2026-08-02T09:00:00Z"
	if err := repo.InsertSale(ctx, nil, InsertSaleParams{
		SaleID: "sale-tbl", ReceiptNo: "000000098", SaleType: "sale", Currency: "GBP",
		Subtotal: 500, Total: 500, CreatedAt: created, TenderType: "cash",
		TableID: tableID, SyncStatus: "synced",
	}); err != nil {
		t.Fatalf("InsertSale: %v", err)
	}
	if err := repo.InsertSaleLine(ctx, nil, "sale-tbl-l1", "sale-tbl", 1, "itm-tbl", "",
		"Table Item", "TBL-SKU", "", 1, 500, 0, 0, 0, 500, 500); err != nil {
		t.Fatalf("InsertSaleLine: %v", err)
	}

	detail, ok, err := repo.GetSaleDetail(ctx, "000000098")
	if err != nil || !ok {
		t.Fatalf("GetSaleDetail: ok=%v err=%v", ok, err)
	}
	if detail.TableID != tableID {
		t.Fatalf("GetSaleDetail.TableID = %q, want %q", detail.TableID, tableID)
	}
	if detail.TableLabel != "T7" {
		t.Fatalf("GetSaleDetail.TableLabel = %q, want %q", detail.TableLabel, "T7")
	}

	byID, ok, err := repo.GetSaleDetailByID(ctx, "sale-tbl")
	if err != nil || !ok {
		t.Fatalf("GetSaleDetailByID: ok=%v err=%v", ok, err)
	}
	if byID.TableLabel != "T7" {
		t.Fatalf("GetSaleDetailByID.TableLabel = %q, want %q", byID.TableLabel, "T7")
	}

	// A sale with no table assigned resolves both fields empty, not an error.
	if err := repo.InsertSale(ctx, nil, InsertSaleParams{
		SaleID: "sale-notbl", ReceiptNo: "000000097", SaleType: "sale", Currency: "GBP",
		Subtotal: 500, Total: 500, CreatedAt: created, TenderType: "cash", SyncStatus: "synced",
	}); err != nil {
		t.Fatalf("InsertSale (no table): %v", err)
	}
	if err := repo.InsertSaleLine(ctx, nil, "sale-notbl-l1", "sale-notbl", 1, "itm-tbl", "",
		"Table Item", "TBL-SKU", "", 1, 500, 0, 0, 0, 500, 500); err != nil {
		t.Fatalf("InsertSaleLine (no table): %v", err)
	}
	noTable, ok, err := repo.GetSaleDetail(ctx, "000000097")
	if err != nil || !ok {
		t.Fatalf("GetSaleDetail (no table): ok=%v err=%v", ok, err)
	}
	if noTable.TableID != "" || noTable.TableLabel != "" {
		t.Fatalf("GetSaleDetail (no table) = id=%q label=%q, want both empty", noTable.TableID, noTable.TableLabel)
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

	// ListRecentSales is unchanged behavior-wise: every row is still this
	// till's local, provenance-less sale (till_id defaults to '' and no
	// till row exists), so TillID/TillName come back empty too.
	for _, e := range entries {
		if e.TillID != "" || e.TillName != "" {
			t.Fatalf("ListRecentSales row has unexpected till provenance: %+v", e)
		}
	}
}

// seedJournalSale inserts a bare sales row for ListSalesJournal tests —
// lighter than seedBatch8Sale (no lines/payments needed, ListSalesJournal
// only reads sales+tills columns).
func seedJournalSale(t *testing.T, d *db.DB, id, receiptNo, tillID, createdAt string, total int64) {
	t.Helper()
	mustExec(t, d, `INSERT INTO sales (id, receipt_no, status, sale_type, tender_type, sync_status, currency, subtotal, total, created_at, till_id)
VALUES (?, ?, 'completed', 'sale', 'cash', 'synced', 'GBP', ?, ?, ?, ?)`,
		id, receiptNo, total, total, createdAt, tillID)
}

// TestPOSRepo_ListSalesJournal_TillFilter covers ut-docs#550: an operator
// filtering the journal to one specific till's sales sees only that till's
// rows, with the other tills' sales excluded.
func TestPOSRepo_ListSalesJournal_TillFilter(t *testing.T) {
	d := openBatch8DB(t, "journal-tillfilter.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)
	mustExec(t, d, `INSERT INTO tills (id, name, bearer_hash) VALUES ('till-2', 'Front Counter', 'bh-2')`)

	seedJournalSale(t, d, "sale-local-1", "L1", "", "2026-08-15T09:00:00Z", 100)
	seedJournalSale(t, d, "sale-t2-1", "T2-1", "till-2", "2026-08-15T09:05:00Z", 200)
	seedJournalSale(t, d, "sale-t2-2", "T2-2", "till-2", "2026-08-15T09:10:00Z", 300)

	entries, _, err := repo.ListSalesJournal(ctx, SalesJournalFilter{TillID: "till-2", Limit: 10})
	if err != nil {
		t.Fatalf("ListSalesJournal(till-2): %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("want 2 entries for till-2, got %d: %+v", len(entries), entries)
	}
	if entries[0].ReceiptNo != "T2-2" || entries[0].TillID != "till-2" || entries[0].TillName != "Front Counter" {
		t.Fatalf("newest till-2 entry mismatch: %+v", entries[0])
	}
	if entries[1].ReceiptNo != "T2-1" {
		t.Fatalf("second till-2 entry mismatch: %+v", entries[1])
	}

	// TillID: "" selects this till's own local (till_id='') sales only.
	local, _, err := repo.ListSalesJournal(ctx, SalesJournalFilter{TillID: "", Limit: 10})
	if err != nil {
		t.Fatalf("ListSalesJournal(local): %v", err)
	}
	if len(local) != 1 || local[0].ReceiptNo != "L1" || local[0].TillID != "" || local[0].TillName != "" {
		t.Fatalf("local-only filter mismatch: %+v", local)
	}
}

// TestPOSRepo_ListSalesJournal_AllTills covers the "All tills" view: rows
// from every till come back, each correctly joined to its till name, and a
// row with till_id=” (this till's own sale) shows an empty TillName rather
// than failing the join.
func TestPOSRepo_ListSalesJournal_AllTills(t *testing.T) {
	d := openBatch8DB(t, "journal-alltills.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)
	mustExec(t, d, `INSERT INTO tills (id, name, bearer_hash) VALUES ('till-2', 'Front Counter', 'bh-2')`)
	mustExec(t, d, `INSERT INTO tills (id, name, bearer_hash) VALUES ('till-3', 'Back Counter', 'bh-3')`)

	seedJournalSale(t, d, "sale-local-1", "L1", "", "2026-08-15T09:00:00Z", 100)
	seedJournalSale(t, d, "sale-t2-1", "T2-1", "till-2", "2026-08-15T09:05:00Z", 200)
	seedJournalSale(t, d, "sale-t3-1", "T3-1", "till-3", "2026-08-15T09:10:00Z", 300)

	entries, _, err := repo.ListSalesJournal(ctx, SalesJournalFilter{AllTills: true, Limit: 10})
	if err != nil {
		t.Fatalf("ListSalesJournal(AllTills): %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("want 3 entries, got %d: %+v", len(entries), entries)
	}
	// Newest first: T3-1, T2-1, L1.
	if entries[0].ReceiptNo != "T3-1" || entries[0].TillID != "till-3" || entries[0].TillName != "Back Counter" {
		t.Fatalf("entry 0 mismatch: %+v", entries[0])
	}
	if entries[1].ReceiptNo != "T2-1" || entries[1].TillID != "till-2" || entries[1].TillName != "Front Counter" {
		t.Fatalf("entry 1 mismatch: %+v", entries[1])
	}
	if entries[2].ReceiptNo != "L1" || entries[2].TillID != "" || entries[2].TillName != "" {
		t.Fatalf("entry 2 (this till) mismatch: %+v", entries[2])
	}
}

// TestPOSRepo_ListSalesJournal_DayFilter covers ut-docs#550's day filter:
// only rows whose created_at falls on the given calendar day are returned.
// The query matches on shop-local calendar day (date(s.created_at,
// 'localtime') = date(?), ut-docs#774 — same convention as DayTotal, not
// SalesByDay's business-day-start shift).
//
// ut-docs#875 (independent review of ut-docs#869, a sibling local-day-
// boundary fix): the original version of this test hardcoded both the
// seeded timestamps and the expected day boundary as fixed UTC literals
// ("2026-08-15"), which only agreed with the production query's
// date(created_at, 'localtime') semantics when the host's local time IS
// UTC — under TZ=Asia/Tokyo the test failed even though ListSalesJournal's
// own day filter is correct. This is the same class of bug ut-docs#559
// already found and fixed elsewhere in this file (see b8ExpectedDay's doc
// comment). Fixed the same way #869's regression tests were: anchor every
// seeded instant on the host's own local noon (time.Now()-derived, not a
// hardcoded literal — noon keeps a same-day instant inside its calendar
// day for any real IANA offset, -12..+14) and derive the expected day
// boundary via SQLite's own date(?, 'localtime') control query
// (b8ExpectedDay), never a Go-side string literal. Verified passing under
// TZ=UTC, TZ=Asia/Tokyo, TZ=America/New_York, TZ=Pacific/Kiritimati,
// TZ=Etc/GMT+12.
func TestPOSRepo_ListSalesJournal_DayFilter(t *testing.T) {
	d := openBatch8DB(t, "journal-dayfilter.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())
	yesterday := today.AddDate(0, 0, -1)

	// Previous local day — must NOT appear in the day-filtered result.
	seedJournalSale(t, d, "sale-d1", "D1", "", b8At(yesterday), 100)
	// Two sales on the target local day.
	seedJournalSale(t, d, "sale-d2", "D2", "", b8At(today), 200)
	seedJournalSale(t, d, "sale-d3", "D3", "", b8At(today.Add(6*time.Hour)), 300)

	day := b8ExpectedDay(t, d, today, 0, 0)
	entries, _, err := repo.ListSalesJournal(ctx, SalesJournalFilter{AllTills: true, Day: day, Limit: 10})
	if err != nil {
		t.Fatalf("ListSalesJournal(day filter): %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("want 2 entries for %s, got %d: %+v", day, len(entries), entries)
	}
	if entries[0].ReceiptNo != "D3" || entries[1].ReceiptNo != "D2" {
		t.Fatalf("day-filtered entries mismatch: %+v", entries)
	}

	// Empty Day = no day filter (falls back to till-scoped only).
	all, _, err := repo.ListSalesJournal(ctx, SalesJournalFilter{AllTills: true, Limit: 10})
	if err != nil {
		t.Fatalf("ListSalesJournal(no day filter): %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("want 3 entries with no day filter, got %d", len(all))
	}
}

// TestPOSRepo_ListSalesJournal_RevokedTill covers B1 from the ut-docs#550
// review: DeleteTill hard-deletes the tills row, but sales.till_id is
// retained -- so a sale journaled from a since-revoked till must still come
// back with TillID set (non-empty) even though the LEFT JOIN can no longer
// resolve a TillName. The caller (journal.html) must not mistake this for
// "this till's own sale" (TillID == ""): a revoked/unknown till still has a
// distinct, non-empty TillID and an empty TillName.
func TestPOSRepo_ListSalesJournal_RevokedTill(t *testing.T) {
	d := openBatch8DB(t, "journal-revoked.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)
	tills := NewTillsRepo(d.DB)

	tillID, err := tills.InsertTill(ctx, "Kiosk Gone", "bh-revoked")
	if err != nil {
		t.Fatalf("InsertTill: %v", err)
	}
	seedJournalSale(t, d, "sale-revoked-1", "REV-1", tillID, "2026-08-15T09:00:00Z", 400)

	if err := tills.DeleteTill(ctx, tillID); err != nil {
		t.Fatalf("DeleteTill: %v", err)
	}

	entries, _, err := repo.ListSalesJournal(ctx, SalesJournalFilter{AllTills: true, Limit: 10})
	if err != nil {
		t.Fatalf("ListSalesJournal: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d: %+v", len(entries), entries)
	}
	if entries[0].ReceiptNo != "REV-1" {
		t.Fatalf("entry mismatch: %+v", entries[0])
	}
	if entries[0].TillID != tillID {
		t.Fatalf("revoked-till sale must retain its TillID, got %q", entries[0].TillID)
	}
	if entries[0].TillName != "" {
		t.Fatalf("revoked-till sale must have an empty TillName (no matching tills row), got %q", entries[0].TillName)
	}
}

// TestPOSRepo_ListSalesJournal_LimitDefault mirrors ListRecentSales' limit<=0
// convention: it defaults to 5.
func TestPOSRepo_ListSalesJournal_LimitDefault(t *testing.T) {
	d := openBatch8DB(t, "journal-limitdefault.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)
	for i := 0; i < 6; i++ {
		seedJournalSale(t, d, "sale-ld"+string(rune('1'+i)), "LD"+string(rune('1'+i)), "",
			"2026-08-15T09:0"+string(rune('0'+i))+":00Z", int64(100+i))
	}
	entries, _, err := repo.ListSalesJournal(ctx, SalesJournalFilter{Limit: 0})
	if err != nil {
		t.Fatalf("ListSalesJournal(limit=0): %v", err)
	}
	if len(entries) != 5 {
		t.Fatalf("want 5 entries (default limit), got %d", len(entries))
	}
}

// TestPOSRepo_ListSalesJournal_Truncated covers ut-docs#774: when more rows
// exist for a filter than the requested limit, the caller needs to know the
// result was capped (to show a "showing the latest N" notice) without a
// separate COUNT(*) query.
func TestPOSRepo_ListSalesJournal_Truncated(t *testing.T) {
	d := openBatch8DB(t, "journal-truncated.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	for i := 0; i < 12; i++ {
		seedJournalSale(t, d, fmt.Sprintf("sale-tr%d", i), fmt.Sprintf("TR%02d", i), "",
			fmt.Sprintf("2026-08-15T%02d:00:00Z", i), int64(100+i))
	}

	entries, truncated, err := repo.ListSalesJournal(ctx, SalesJournalFilter{AllTills: true, Limit: 10})
	if err != nil {
		t.Fatalf("ListSalesJournal(limit 10 of 12): %v", err)
	}
	if len(entries) != 10 {
		t.Fatalf("want exactly 10 entries (capped at limit), got %d", len(entries))
	}
	if !truncated {
		t.Fatalf("want truncated=true when 12 rows exist for limit 10")
	}

	entries, truncated, err = repo.ListSalesJournal(ctx, SalesJournalFilter{AllTills: true, Limit: 20})
	if err != nil {
		t.Fatalf("ListSalesJournal(limit 20 of 12): %v", err)
	}
	if len(entries) != 12 {
		t.Fatalf("want all 12 entries under a 20 limit, got %d", len(entries))
	}
	if truncated {
		t.Fatalf("want truncated=false when fewer rows exist than the limit")
	}

	// Exact boundary: rows == limit must NOT be flagged truncated (fencepost).
	entries, truncated, err = repo.ListSalesJournal(ctx, SalesJournalFilter{AllTills: true, Limit: 12})
	if err != nil {
		t.Fatalf("ListSalesJournal(limit 12 of 12): %v", err)
	}
	if len(entries) != 12 {
		t.Fatalf("want all 12 entries at the exact limit, got %d", len(entries))
	}
	if truncated {
		t.Fatalf("want truncated=false when rows exactly equal the limit (fencepost)")
	}
}
