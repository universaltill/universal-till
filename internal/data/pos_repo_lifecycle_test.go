package data

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/db"
)

// newPOSLifecycleTestDB opens a fresh DB via the real migrations (shifts,
// report_archive, sale_links, registers, tills, etc. all need the real
// schema — a hand-rolled per-file schema would be a lot of duplication for
// this many tables) and clears the seeded demo data that migrations insert,
// so each test starts from a known, empty state.
func newPOSLifecycleTestDB(t *testing.T) *posTestDB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "pos_lifecycle.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	ctx := context.Background()
	for _, tbl := range []string{"payments", "sale_links", "sale_discounts", "sale_lines", "sales", "shifts", "shortcut_buttons", "inventory"} {
		if _, err := d.DB.ExecContext(ctx, `DELETE FROM `+tbl); err != nil {
			t.Fatalf("clear seeded %s: %v", tbl, err)
		}
	}
	// shifts.register_id/cashier_id and audit_log.actor_id are real foreign
	// keys — migrations seed sample catalog data but not registers/users/
	// stock_locations, so tests that reference reg1/user1/user2/loc1 need
	// them created first.
	seeds := []string{
		`INSERT INTO registers(id,name,is_active) VALUES('reg1','Front Till',1)`,
		`INSERT INTO users(id,username,display_name,pin_hash,role,is_active) VALUES('user1','user1','User One','','cashier',1)`,
		`INSERT INTO users(id,username,display_name,pin_hash,role,is_active) VALUES('user2','user2','User Two','','cashier',1)`,
		`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`,
	}
	for _, s := range seeds {
		if _, err := d.DB.ExecContext(ctx, s); err != nil {
			t.Fatalf("seed fixture %q: %v", s, err)
		}
	}
	return &posTestDB{d: d, repo: NewPOSRepo(d.DB)}
}

// posTestDB bundles the opened DB and its repo — avoids importing *sql.DB
// directly into every test while keeping raw access for seeding.
type posTestDB struct {
	d    *db.DB
	repo *POSRepo
}

func seedLifecycleSale(t *testing.T, dbx *posTestDB, id, receiptNo, saleType, status, createdAt string, total, taxTotal int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := dbx.d.DB.ExecContext(ctx, `INSERT INTO sales(id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at, completed_at)
VALUES(?, ?, ?, ?, 'GBP', ?, 0, ?, ?, ?, ?)`, id, receiptNo, status, saleType, total-taxTotal, taxTotal, total, createdAt, createdAt); err != nil {
		t.Fatalf("seed sale %s: %v", id, err)
	}
}

func TestGetShiftOpeningCash(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	if _, err := dbx.d.DB.ExecContext(ctx,
		`INSERT INTO shifts(id, register_id, cashier_id, opened_at, opening_cash) VALUES('shift1','reg1','user1',datetime('now'),5000)`); err != nil {
		t.Fatal(err)
	}

	cash, ok, err := dbx.repo.GetShiftOpeningCash(ctx, "shift1")
	if err != nil || !ok || cash != 5000 {
		t.Fatalf("expected opening cash 5000, got cash=%d ok=%v err=%v", cash, ok, err)
	}

	if _, ok, err := dbx.repo.GetShiftOpeningCash(ctx, "missing"); err != nil || ok {
		t.Fatalf("expected ok=false for an unknown shift, got ok=%v err=%v", ok, err)
	}

	// A CLOSED shift must not be returned (query filters closed_at IS NULL).
	if _, err := dbx.d.DB.ExecContext(ctx,
		`UPDATE shifts SET closed_at = datetime('now') WHERE id = 'shift1'`); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := dbx.repo.GetShiftOpeningCash(ctx, "shift1"); err != nil || ok {
		t.Fatalf("expected ok=false for a closed shift, got ok=%v err=%v", ok, err)
	}
}

func TestCurrentOpenShift(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	if _, _, err := dbx.repo.CurrentOpenShift(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := dbx.repo.CurrentOpenShift(ctx); err != nil || ok {
		t.Fatalf("expected no open shift initially, got ok=%v err=%v", ok, err)
	}

	if _, err := dbx.d.DB.ExecContext(ctx,
		`INSERT INTO shifts(id, register_id, cashier_id, opened_at, opening_cash) VALUES('shift1','reg1','user1','2026-01-01T09:00:00Z',5000)`); err != nil {
		t.Fatal(err)
	}
	s, ok, err := dbx.repo.CurrentOpenShift(ctx)
	if err != nil || !ok {
		t.Fatalf("expected an open shift, got ok=%v err=%v", ok, err)
	}
	if !s.Open || s.ID != "shift1" || s.OpeningCash != 5000 {
		t.Fatalf("unexpected shift summary: %+v", s)
	}

	// A later-opened shift wins (ORDER BY opened_at DESC).
	if _, err := dbx.d.DB.ExecContext(ctx,
		`INSERT INTO shifts(id, register_id, cashier_id, opened_at, opening_cash) VALUES('shift2','reg1','user2','2026-01-02T09:00:00Z',6000)`); err != nil {
		t.Fatal(err)
	}
	s, ok, err = dbx.repo.CurrentOpenShift(ctx)
	if err != nil || !ok || s.ID != "shift2" {
		t.Fatalf("expected the most recently opened shift (shift2), got %+v ok=%v err=%v", s, ok, err)
	}
}

// TestCurrentOpenShiftForRegister covers the register-scoped resolution added
// for ut-docs#268: with two concurrent open shifts on two different registers,
// each register must resolve to ITS OWN shift — never "whichever was opened
// most recently anywhere" — and a register with no open shift must come back
// ok=false even while another register's shift is open.
func TestCurrentOpenShiftForRegister(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	if _, err := dbx.d.DB.ExecContext(ctx,
		`INSERT INTO registers(id,name,is_active) VALUES('reg2','Back Till',1)`); err != nil {
		t.Fatal(err)
	}

	// No shifts at all: ok=false, no error.
	if _, ok, err := dbx.repo.CurrentOpenShiftForRegister(ctx, "reg1"); err != nil || ok {
		t.Fatalf("expected no open shift initially, got ok=%v err=%v", ok, err)
	}

	// Open a shift on reg1, then a LATER one on reg2 — the later shift is the
	// one the old any-register heuristic would return for everyone.
	if _, err := dbx.d.DB.ExecContext(ctx,
		`INSERT INTO shifts(id, register_id, cashier_id, opened_at, opening_cash) VALUES('shift1','reg1','user1','2026-01-01T09:00:00Z',5000)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dbx.d.DB.ExecContext(ctx,
		`INSERT INTO shifts(id, register_id, cashier_id, opened_at, opening_cash) VALUES('shift2','reg2','user2','2026-01-02T09:00:00Z',6000)`); err != nil {
		t.Fatal(err)
	}

	s, ok, err := dbx.repo.CurrentOpenShiftForRegister(ctx, "reg1")
	if err != nil || !ok {
		t.Fatalf("expected an open shift for reg1, got ok=%v err=%v", ok, err)
	}
	if s.ID != "shift1" || s.RegisterID != "reg1" || !s.Open || s.OpeningCash != 5000 {
		t.Fatalf("expected reg1's own shift1, got %+v", s)
	}

	s, ok, err = dbx.repo.CurrentOpenShiftForRegister(ctx, "reg2")
	if err != nil || !ok || s.ID != "shift2" || s.RegisterID != "reg2" {
		t.Fatalf("expected reg2's own shift2, got %+v ok=%v err=%v", s, ok, err)
	}

	// A register with no open shift resolves to nothing even while another
	// register's shift is open.
	if _, err := dbx.d.DB.ExecContext(ctx,
		`UPDATE shifts SET closed_at = '2026-01-02T17:00:00Z' WHERE id = 'shift1'`); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := dbx.repo.CurrentOpenShiftForRegister(ctx, "reg1"); err != nil || ok {
		t.Fatalf("expected ok=false for reg1 after closing its shift, got ok=%v err=%v", ok, err)
	}
}

func TestListRecentShifts(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	if _, err := dbx.d.DB.ExecContext(ctx,
		`INSERT INTO shifts(id, register_id, cashier_id, opened_at, closed_at, opening_cash, closing_cash, expected_cash) VALUES('shift1','reg1','user1','2026-01-01T09:00:00Z','2026-01-01T17:00:00Z',5000,7000,7500)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dbx.d.DB.ExecContext(ctx,
		`INSERT INTO shifts(id, register_id, cashier_id, opened_at, opening_cash) VALUES('shift2','reg1','user2','2026-01-02T09:00:00Z',6000)`); err != nil {
		t.Fatal(err)
	}

	shifts, err := dbx.repo.ListRecentShifts(ctx, 0) // limit<=0 defaults to 20
	if err != nil {
		t.Fatal(err)
	}
	if len(shifts) != 2 {
		t.Fatalf("expected 2 shifts, got %d", len(shifts))
	}
	// Newest opened_at first.
	if shifts[0].ID != "shift2" || !shifts[0].Open {
		t.Fatalf("expected shift2 (still open) first, got %+v", shifts[0])
	}
	closed := shifts[1]
	if closed.ID != "shift1" || closed.Open {
		t.Fatalf("expected shift1 closed, got %+v", closed)
	}
	// Variance is only computed for closed shifts: 7000 - 7500 = -500.
	if closed.Variance != -500 {
		t.Fatalf("expected variance -500 (closing 7000 - expected 7500), got %d", closed.Variance)
	}
	if shifts[0].Variance != 0 {
		t.Fatalf("expected zero variance for an open shift, got %d", shifts[0].Variance)
	}
}

func TestListRegisters_OnlyActive(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	if _, err := dbx.d.DB.ExecContext(ctx, `DELETE FROM registers`); err != nil {
		t.Fatal(err)
	}
	if _, err := dbx.d.DB.ExecContext(ctx, `INSERT INTO registers(id,name,is_active) VALUES('reg1','Front Till',1),('reg2','Retired Till',0)`); err != nil {
		t.Fatal(err)
	}

	regs, err := dbx.repo.ListRegisters(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(regs) != 1 || regs[0].ID != "reg1" {
		t.Fatalf("expected only the active register, got %+v", regs)
	}
}

func TestInsertSaleDiscountAndLink(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	seedLifecycleSale(t, dbx, "sale1", "R1", "sale", "completed", "2026-01-01T10:00:00Z", 100, 20)

	if err := dbx.repo.InsertSaleDiscount(ctx, nil, "disc1", "sale1", "", "percent", 10, 10, "loyalty"); err != nil {
		t.Fatal(err)
	}
	var discType string
	var amount int64
	if err := dbx.d.DB.QueryRowContext(ctx, `SELECT type, amount FROM sale_discounts WHERE id = ?`, "disc1").Scan(&discType, &amount); err != nil {
		t.Fatalf("expected the discount row, got %v", err)
	}
	if discType != "percent" || amount != 10 {
		t.Fatalf("unexpected discount row: type=%q amount=%d", discType, amount)
	}

	seedLifecycleSale(t, dbx, "return1", "R2", "return", "completed", "2026-01-01T11:00:00Z", 50, 10)
	if err := dbx.repo.InsertSaleLink(ctx, nil, "link1", "return1", "sale1", "faulty item"); err != nil {
		t.Fatal(err)
	}
	orig, err := dbx.repo.OriginalSaleIDFor(ctx, "return1")
	if err != nil || orig != "sale1" {
		t.Fatalf("expected sale1 as the original sale, got %q err=%v", orig, err)
	}
	// A sale with no link at all: empty string, not an error.
	if orig, err := dbx.repo.OriginalSaleIDFor(ctx, "sale1"); err != nil || orig != "" {
		t.Fatalf("expected empty original sale id for a plain sale, got %q err=%v", orig, err)
	}
}

func TestUpsertInventory_UpdatesThenInsertsOnMiss(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	if _, err := dbx.d.DB.ExecContext(ctx, `INSERT INTO items(id,sku,name,base_price,is_active,is_weighed,unit) VALUES('itm1','SKU1','Apple',100,1,0,'each')`); err != nil {
		t.Fatal(err)
	}

	// No existing row: UPDATE affects 0 rows, falls back to INSERT.
	if err := dbx.repo.UpsertInventory(ctx, nil, "loc1", "itm1", "", 10, "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	var qty float64
	if err := dbx.d.DB.QueryRowContext(ctx, `SELECT quantity FROM inventory WHERE item_id='itm1' AND location_id='loc1'`).Scan(&qty); err != nil {
		t.Fatalf("expected an inventory row created, got %v", err)
	}
	if qty != 10 {
		t.Fatalf("expected quantity 10, got %v", qty)
	}

	// Existing row: UPDATE adds the delta (not a replace).
	if err := dbx.repo.UpsertInventory(ctx, nil, "loc1", "itm1", "", -3, "2026-01-02T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := dbx.d.DB.QueryRowContext(ctx, `SELECT quantity FROM inventory WHERE item_id='itm1' AND location_id='loc1'`).Scan(&qty); err != nil {
		t.Fatal(err)
	}
	if qty != 7 {
		t.Fatalf("expected quantity 10-3=7, got %v", qty)
	}
}

func TestListPaymentMethodIDs_ActiveOnlyDeduped(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	if _, err := dbx.d.DB.ExecContext(ctx, `DELETE FROM payment_methods`); err != nil {
		t.Fatal(err)
	}
	if _, err := dbx.d.DB.ExecContext(ctx,
		`INSERT INTO payment_methods(id,name,type,is_active) VALUES('cash','Cash','cash',1),('card','Card','card',1),('old','Old','cash',0)`); err != nil {
		t.Fatal(err)
	}

	ids, err := dbx.repo.ListPaymentMethodIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Fatalf("expected 2 active methods, got %+v", ids)
	}
}

func TestReturnChain_ReceiptsAndReturnedQuantities(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	if _, err := dbx.d.DB.ExecContext(ctx, `INSERT INTO items(id,sku,name,base_price,is_active,is_weighed,unit) VALUES('itm1','SKU1','Apple',100,1,0,'each')`); err != nil {
		t.Fatal(err)
	}
	seedLifecycleSale(t, dbx, "sale1", "R-ORIG", "sale", "completed", "2026-01-01T10:00:00Z", 220, 20)
	if _, err := dbx.d.DB.ExecContext(ctx, `INSERT INTO sale_lines(id, sale_id, line_no, item_id, name_snapshot, sku_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
VALUES('line1','sale1',1,'itm1','Apple','SKU1',2,100,2000,20,200,220)`); err != nil {
		t.Fatal(err)
	}

	// ReceiptExists / ReceiptNo lookups before any return exists.
	if ok, err := dbx.repo.ReceiptExists(ctx, "R-ORIG"); err != nil || !ok {
		t.Fatalf("expected the original receipt to exist, got ok=%v err=%v", ok, err)
	}
	if ok, err := dbx.repo.ReceiptExists(ctx, "NO-SUCH"); err != nil || ok {
		t.Fatalf("expected an unknown receipt not to exist, got ok=%v err=%v", ok, err)
	}
	if receipts, err := dbx.repo.ReturnReceiptsFor(ctx, "sale1"); err != nil || len(receipts) != 0 {
		t.Fatalf("expected no returns yet, got %+v err=%v", receipts, err)
	}
	if _, ok, err := dbx.repo.OriginalReceiptFor(ctx, "sale1"); err != nil || ok {
		t.Fatalf("expected sale1 itself to have no 'original receipt' (it's not a return), got ok=%v err=%v", ok, err)
	}

	// Record a partial return of 1 of the 2 units.
	seedLifecycleSale(t, dbx, "return1", "R-RETURN", "return", "completed", "2026-01-01T11:00:00Z", 110, 10)
	if _, err := dbx.d.DB.ExecContext(ctx, `INSERT INTO sale_lines(id, sale_id, line_no, item_id, name_snapshot, sku_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
VALUES('rline1','return1',1,'itm1','Apple','SKU1',1,100,2000,10,100,110)`); err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.InsertSaleLink(ctx, nil, "link1", "return1", "sale1", "damaged"); err != nil {
		t.Fatal(err)
	}

	receipt, ok, err := dbx.repo.OriginalReceiptFor(ctx, "return1")
	if err != nil || !ok || receipt != "R-ORIG" {
		t.Fatalf("expected R-ORIG as the original receipt, got %q ok=%v err=%v", receipt, ok, err)
	}
	receipts, err := dbx.repo.ReturnReceiptsFor(ctx, "sale1")
	if err != nil || len(receipts) != 1 || receipts[0] != "R-RETURN" {
		t.Fatalf("expected [R-RETURN], got %+v err=%v", receipts, err)
	}

	returned, err := dbx.repo.ReturnedQuantities(ctx, "sale1")
	if err != nil {
		t.Fatal(err)
	}
	key := RefundLineKey("itm1", "", 100, "")
	if returned[key] != 1 {
		t.Fatalf("expected 1 unit already returned at key %q, got %+v", key, returned)
	}
}

func TestSaleExistsAndSetSaleProvenance(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	seedLifecycleSale(t, dbx, "sale1", "R1", "sale", "completed", "2026-01-01T10:00:00Z", 100, 20)

	if ok, err := dbx.repo.SaleExists(ctx, "sale1"); err != nil || !ok {
		t.Fatalf("expected sale to exist, got ok=%v err=%v", ok, err)
	}
	if ok, err := dbx.repo.SaleExists(ctx, "missing"); err != nil || ok {
		t.Fatalf("expected sale not to exist, got ok=%v err=%v", ok, err)
	}

	// SetSaleProvenance stamps a journaled-in (cloud-synced) sale with its
	// originating till and true creation time.
	if err := dbx.repo.SetSaleProvenance(ctx, "sale1", "till-remote-1", "2025-12-25T08:00:00Z"); err != nil {
		t.Fatal(err)
	}
	var tillID, createdAt string
	if err := dbx.d.DB.QueryRowContext(ctx, `SELECT till_id, created_at FROM sales WHERE id='sale1'`).Scan(&tillID, &createdAt); err != nil {
		t.Fatal(err)
	}
	if tillID != "till-remote-1" || createdAt != "2025-12-25T08:00:00Z" {
		t.Fatalf("provenance not applied: till_id=%q created_at=%q", tillID, createdAt)
	}
}

func TestLocalSalesSinceAndCount(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	// A local sale (till_id = '') and a journaled-in one (till_id != '') —
	// only the local one belongs in the push queue.
	seedLifecycleSale(t, dbx, "local1", "R-LOCAL", "sale", "completed", "2026-01-01T10:00:00Z", 100, 20)
	seedLifecycleSale(t, dbx, "remote1", "R-REMOTE", "sale", "completed", "2026-01-01T11:00:00Z", 100, 20)
	if _, err := dbx.d.DB.ExecContext(ctx, `UPDATE sales SET till_id = 'other-till' WHERE id = 'remote1'`); err != nil {
		t.Fatal(err)
	}

	sales, err := dbx.repo.LocalSalesSince(ctx, "2026-01-01T00:00:00Z", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(sales) != 1 || sales[0] != "R-LOCAL" {
		t.Fatalf("expected only the local sale's receipt, got %+v", sales)
	}

	n, err := dbx.repo.CountLocalSalesSince(ctx, "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected count 1, got %d", n)
	}
}

func TestArchiveReport_IdempotentPerKindAndPeriod(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	created, err := dbx.repo.ArchiveReport(ctx, "eod", "2026-01-01", []byte(`{"total":100}`), "", "", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("expected the first archive to report created=true")
	}

	// Same kind+period again: no-op, reports created=false, doesn't clobber.
	created, err = dbx.repo.ArchiveReport(ctx, "eod", "2026-01-01", []byte(`{"total":999}`), "", "", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("expected the second archive of the same kind+period to report created=false")
	}

	if has, err := dbx.repo.HasArchivedReport(ctx, "eod", "2026-01-01"); err != nil || !has {
		t.Fatalf("expected HasArchivedReport=true, got %v err=%v", has, err)
	}
	if has, err := dbx.repo.HasArchivedReport(ctx, "eod", "2026-01-02"); err != nil || has {
		t.Fatalf("expected HasArchivedReport=false for a different period, got %v err=%v", has, err)
	}

	reports, err := dbx.repo.ListArchivedReports(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 1 || reports[0].Content != `{"total":100}` {
		t.Fatalf("expected the original (un-clobbered) content, got %+v", reports)
	}
}

// ADR-0040 card 1: till-mode age-based prune, coverage summary and the
// bounded-range export fetch.

func TestPruneReportArchiveOlderThan_DeletesOnlyRowsBeforeCutoff(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	for _, p := range []string{"2015-06-01", "2015-12-31", "2016-01-01", "2026-01-01"} {
		if _, err := dbx.repo.ArchiveReport(ctx, "eod", p, []byte(`{}`), "", "", time.Time{}); err != nil {
			t.Fatalf("seed archive %s: %v", p, err)
		}
	}

	n, err := dbx.repo.PruneReportArchiveOlderThan(ctx, "2016-01-01")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("expected 2 rows pruned (strictly before cutoff), got %d", n)
	}

	reports, err := dbx.repo.ListArchivedReports(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 2 {
		t.Fatalf("expected 2 rows remaining, got %d: %+v", len(reports), reports)
	}
	for _, r := range reports {
		if r.Period < "2016-01-01" {
			t.Fatalf("row before cutoff survived prune: %+v", r)
		}
	}

	// A second run at the same cutoff is idempotent -- nothing left to prune.
	n, err = dbx.repo.PruneReportArchiveOlderThan(ctx, "2016-01-01")
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("expected 0 rows pruned on the second run, got %d", n)
	}
}

func TestReportArchiveCoverage_EmptyAndPopulated(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	cov, err := dbx.repo.ReportArchiveCoverage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cov.Count != 0 || cov.Earliest != "" || cov.Latest != "" {
		t.Fatalf("expected zero-value coverage on an empty archive, got %+v", cov)
	}

	for _, p := range []string{"2024-03-01", "2025-01-15", "2023-11-30"} {
		if _, err := dbx.repo.ArchiveReport(ctx, "eod", p, []byte(`{}`), "", "", time.Time{}); err != nil {
			t.Fatalf("seed archive %s: %v", p, err)
		}
	}

	cov, err = dbx.repo.ReportArchiveCoverage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cov.Count != 3 || cov.Earliest != "2023-11-30" || cov.Latest != "2025-01-15" {
		t.Fatalf("expected earliest=2023-11-30 latest=2025-01-15 count=3, got %+v", cov)
	}
}

func TestArchivedReportsInRange_BoundedAndOrdered(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	// ADR-0066 Decision 5: the range filter moved off a `period BETWEEN`
	// text compare onto each row's own created_at, so every seeded row
	// needs a genuinely distinct created_at (an explicit closedAt) -- with
	// a zero closedAt every row here would share the SAME real-now
	// created_at regardless of what period text it was given, and the
	// test would no longer prove anything about range bounding. Anchored
	// on the host's own real "now" (never a hardcoded year) so this
	// doesn't rot; days spaced far enough apart that the "eod" kind's
	// atomic same-local-day guard (ADR-0066 Decision 4) never fires
	// between them.
	// LOCAL noon, never UTC (2026-09-04 review of ut-docs#1141): the range
	// filter compares datetime(created_at, 'localtime') against these same
	// LOCAL date bounds, so a UTC-anchored seed would encode UTC-day
	// semantics and flip this test's meaning on a non-UTC host — the exact
	// mistake eod_zreport_local_day_869_test.go's own doc comment records.
	// Noon keeps every same-day instant safely inside its calendar day for
	// any real IANA offset (-12..+14).
	anchor := time.Now()
	at := func(daysAgo int) time.Time {
		return time.Date(anchor.Year(), anchor.Month(), anchor.Day(), 12, 0, 0, 0, time.Local).AddDate(0, 0, -daysAgo)
	}
	seed := func(daysAgo int) string {
		closedAt := at(daysAgo)
		period := closedAt.Format("2006-01-02")
		if _, err := dbx.repo.ArchiveReport(ctx, "eod", period, []byte(`{"p":"`+period+`"}`), "", "", closedAt); err != nil {
			t.Fatalf("seed archive %s: %v", period, err)
		}
		return period
	}
	pLower := seed(10) // range's lower bound itself: included
	pMid := seed(6)    // inside: included
	seed(1)            // after the range's upper bound: excluded
	seed(11)           // before the range's lower bound: excluded

	from := at(10).Format("2006-01-02")
	to := at(2).Format("2006-01-02")
	rows, err := dbx.repo.ArchivedReportsInRange(ctx, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows in range, got %d: %+v", len(rows), rows)
	}
	if rows[0].Period != pLower || rows[1].Period != pMid {
		t.Fatalf("expected rows ordered by period (%s, %s), got %+v", pLower, pMid, rows)
	}
}

// TestArchivedReportsInRange_NewFormatPeriodOnRangeLastDayNotDropped pins
// ADR-0066 Decision 5's own named regression directly at the query layer:
// "an RFC3339 period falling on the export range's own last day sorts
// after that bare date bound and is silently excluded." A pre-cutover
// legacy row (bare calendar-date period) and a post-cutover row (RFC3339
// close-instant period, via a real closedAt) land on DIFFERENT days within
// the same range -- the new-format row on the range's own last day, where
// the old `period BETWEEN` text compare would have wrongly excluded it
// (an RFC3339 string like "2026-08-24T21:00:00Z" sorts after the bare
// date bound "2026-08-24"). Both must come back.
func TestArchivedReportsInRange_NewFormatPeriodOnRangeLastDayNotDropped(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	// Legacy row, well inside the range but not on its last day -- direct
	// SQL, not ArchiveReport with a zero closedAt, so its created_at is
	// exactly this literal rather than whatever "now" happens to be.
	if _, err := dbx.d.DB.ExecContext(ctx, `INSERT INTO report_archive (id, kind, period, content_json, created_at)
VALUES ('legacy-lastday', 'eod', '2026-08-10', '{}', '2026-08-10 20:00:00')`); err != nil {
		t.Fatal(err)
	}

	// New-format row landing exactly on the range's own last day. LOCAL
	// noon, and a local-offset RFC3339 period (exactly what generateEOD
	// writes) — the range filter compares against the shop's local day, so
	// a UTC-literal seed would encode UTC-day semantics and flip this
	// test's meaning on a non-UTC host (2026-09-04 review of ut-docs#1141;
	// same lesson eod_zreport_local_day_869_test.go's doc comment records).
	// The regression this test pins is unaffected: a local-offset RFC3339
	// period like "2026-08-24T12:00:00+14:00" still sorts AFTER the bare
	// date bound "2026-08-24" under the old `period BETWEEN` filter.
	newClosedAt := time.Date(2026, 8, 24, 12, 0, 0, 0, time.Local)
	newPeriod := newClosedAt.Format(time.RFC3339)
	created, err := dbx.repo.ArchiveReport(ctx, "eod", newPeriod, []byte(`{}`), "", "", newClosedAt)
	if err != nil || !created {
		t.Fatalf("archive new-format close: created=%v err=%v", created, err)
	}

	rows, err := dbx.repo.ArchivedReportsInRange(ctx, "2026-08-01", "2026-08-24")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected both the legacy and new-format close in range, got %d: %+v", len(rows), rows)
	}
	var gotLegacy, gotNew bool
	for _, r := range rows {
		if r.Period == "2026-08-10" {
			gotLegacy = true
		}
		if r.Period == newPeriod {
			gotNew = true
		}
	}
	if !gotLegacy || !gotNew {
		t.Fatalf("expected both periods present, got legacy=%v new=%v rows=%+v", gotLegacy, gotNew, rows)
	}
}

// TestArchivedReportsInRange_ComparesLocalDayNotUTCDay is the review finding
// on ADR-0066 Decision 5's export-filter change (2026-09-04 review of
// ut-docs#1141): moving the filter off `period BETWEEN` onto created_at is
// right, but created_at is stored UTC-naive (schema default datetime('now'),
// or ArchiveReport's closedAt.UTC() write) while from/to are LOCAL calendar
// dates typed by the shop owner — the same dates the row's own `period`
// string now displays in the shop's local offset. Comparing the two without
// 'localtime' silently reinterprets the requested range as a UTC day window,
// so a close made just after LOCAL midnight is missing from an export for
// its own local day and instead turns up under the previous one. That is the
// exact bug class ADR-0057 exists to remove, and the exact comparison
// ArchiveReport's own double-close guard already gets right (see
// TestArchiveReport_GuardComparesLocalDayNotUTCDay, whose subprocess-TZ
// technique and rationale this test mirrors — modernc.org/sqlite's
// 'localtime' resolves from the process TZ, cached on first use, so only a
// genuine fresh process with TZ pre-set proves anything here).
func TestArchivedReportsInRange_ComparesLocalDayNotUTCDay(t *testing.T) {
	if os.Getenv("UT_TEST_RANGE_TZ") == "1" {
		runArchivedReportsInRangeLocalDayBody(t)
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestArchivedReportsInRange_ComparesLocalDayNotUTCDay$", "-test.v")
	cmd.Env = append(os.Environ(), "UT_TEST_RANGE_TZ=1", "TZ=Pacific/Kiritimati")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--- PASS") {
		t.Fatalf("subprocess (TZ=Pacific/Kiritimati) failed: err=%v\n%s", err, out)
	}
}

func runArchivedReportsInRangeLocalDayBody(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	// A close just after LOCAL midnight on 2026-08-27 (UTC+14), i.e. still
	// on 2026-08-26 in UTC. generateEOD would store its period as the
	// local-offset instant "2026-08-27T01:00:00+14:00" — so 2026-08-27 is
	// the date the shop owner sees and the date they would ask an export
	// for.
	closedAt := time.Date(2026, 8, 26, 11, 0, 0, 0, time.UTC) // local 2026-08-27 01:00
	period := closedAt.In(time.FixedZone("UTC+14", 14*60*60)).Format(time.RFC3339)
	created, err := dbx.repo.ArchiveReport(ctx, "eod", period, []byte(`{}`), "", "", closedAt)
	if err != nil || !created {
		t.Fatalf("archive after-local-midnight close: created=%v err=%v", created, err)
	}

	rows, err := dbx.repo.ArchivedReportsInRange(ctx, "2026-08-27", "2026-08-27")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Period != period {
		t.Fatalf("want the close returned for an export of its OWN local day 2026-08-27 (period %s); "+
			"a comparison without 'localtime' reads the range as a UTC day and drops it, got %+v", period, rows)
	}

	// And it must NOT also answer for the previous local day — the window
	// is genuinely shifted, not merely widened.
	rows, err = dbx.repo.ArchivedReportsInRange(ctx, "2026-08-26", "2026-08-26")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("want no rows for local day 2026-08-26 (the close belongs to 2026-08-27 locally), got %+v", rows)
	}
}

func TestEndOfDay_AggregatesSalesReturnsAndMethods(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	// Anchored on the host's own local noon, not a hardcoded UTC date
	// literal (ut-docs#869 independent review finding): EndOfDay now
	// matches the LOCAL calendar day (date(created_at, 'localtime')), so a
	// fixed "2026-01-01T10:00:00Z"-style literal only holds together on a
	// UTC host — under TZ=Asia/Tokyo this test regressed red. Local noon
	// keeps every same-day instant inside its calendar day for any real
	// IANA offset (-12..+14); the day argument is derived via the same
	// date(?, 'localtime') control query production uses (b8ExpectedDay),
	// never a Go-side literal.
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())
	tomorrow := today.AddDate(0, 0, 1)

	seedLifecycleSale(t, dbx, "sale1", "R001", "sale", "completed", b8At(today), 220, 20)
	seedLifecycleSale(t, dbx, "sale2", "R002", "sale", "completed", b8At(today.Add(4*time.Hour)), 110, 10)
	seedLifecycleSale(t, dbx, "return1", "R003", "return", "completed", b8At(today.Add(5*time.Hour)), 55, 5)
	// A sale on a different day must not be included.
	seedLifecycleSale(t, dbx, "sale3", "R004", "sale", "completed", b8At(tomorrow), 1000, 100)
	if _, err := dbx.d.DB.ExecContext(ctx, `INSERT INTO payments(id, sale_id, method_id, amount, currency, change_given, paid_at) VALUES
('pay1','sale1','cash',220,'GBP',0,?),
('pay2','sale2','card',110,'GBP',0,?),
('pay3','return1','cash',55,'GBP',0,?)`, b8At(today), b8At(today.Add(4*time.Hour)), b8At(today.Add(5*time.Hour))); err != nil {
		t.Fatal(err)
	}

	day := b8ExpectedDay(t, dbx.d, today, 0, 0)
	rep, err := dbx.repo.EndOfDay(ctx, day)
	if err != nil {
		t.Fatal(err)
	}
	if rep.SalesCount != 2 {
		t.Fatalf("expected 2 sales, got %d", rep.SalesCount)
	}
	if rep.Gross != 330 {
		t.Fatalf("expected gross 220+110=330, got %d", rep.Gross)
	}
	if rep.RefundCount != 1 || rep.RefundTotal != 55 {
		t.Fatalf("expected 1 refund totalling 55, got count=%d total=%d", rep.RefundCount, rep.RefundTotal)
	}
	if rep.Net != 330-55 {
		t.Fatalf("expected net = gross - refunds = 275, got %d", rep.Net)
	}
	if rep.FirstReceipt != "R001" || rep.LastReceipt != "R003" {
		t.Fatalf("expected receipt range R001..R003, got %q..%q", rep.FirstReceipt, rep.LastReceipt)
	}
	if len(rep.Methods) != 2 {
		t.Fatalf("expected 2 payment methods (cash, card), got %+v", rep.Methods)
	}
}

func TestEndOfDayRange_AggregatesAcrossMultipleDaysInclusive(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	// Same local-noon-anchored, host-timezone-safe shape as
	// TestEndOfDay_AggregatesSalesReturnsAndMethods above (ut-docs#869
	// independent review finding) — day1/day2/day3 are three consecutive
	// local calendar days; before/after sit a full local day outside
	// either end of the range, safely clear of the boundary for any real
	// IANA offset.
	now := time.Now()
	day1 := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())
	day2 := day1.AddDate(0, 0, 1)
	day3 := day1.AddDate(0, 0, 2)
	before := day1.AddDate(0, 0, -1)
	after := day3.AddDate(0, 0, 1)

	seedLifecycleSale(t, dbx, "sale1", "R001", "sale", "completed", b8At(day1), 220, 20)
	seedLifecycleSale(t, dbx, "sale2", "R002", "sale", "completed", b8At(day2), 110, 10)
	seedLifecycleSale(t, dbx, "return1", "R003", "return", "completed", b8At(day3), 55, 5)
	// Outside the requested range on both ends — must not be included.
	seedLifecycleSale(t, dbx, "before", "R000", "sale", "completed", b8At(before), 9999, 0)
	seedLifecycleSale(t, dbx, "after", "R004", "sale", "completed", b8At(after), 9999, 0)

	fromDay := b8ExpectedDay(t, dbx.d, day1, 0, 0)
	toDay := b8ExpectedDay(t, dbx.d, day3, 0, 0)
	rep, err := dbx.repo.EndOfDayRange(ctx, fromDay, toDay)
	if err != nil {
		t.Fatal(err)
	}
	if rep.From != fromDay || rep.To != toDay {
		t.Fatalf("expected From/To echoed back, got %q..%q", rep.From, rep.To)
	}
	if rep.Day != "" {
		t.Fatalf("expected Day empty for a range report, got %q", rep.Day)
	}
	if rep.SalesCount != 2 {
		t.Fatalf("expected 2 sales (day 1 + day 2, excluding day-3 return and out-of-range rows), got %d", rep.SalesCount)
	}
	if rep.Gross != 330 {
		t.Fatalf("expected gross 220+110=330, got %d", rep.Gross)
	}
	if rep.RefundCount != 1 || rep.RefundTotal != 55 {
		t.Fatalf("expected the day-3 return (inclusive upper bound) counted: 1 refund totalling 55, got count=%d total=%d", rep.RefundCount, rep.RefundTotal)
	}
	if rep.FirstReceipt != "R001" || rep.LastReceipt != "R003" {
		t.Fatalf("expected receipt range R001..R003 (inclusive both ends), got %q..%q", rep.FirstReceipt, rep.LastReceipt)
	}
}

func TestEndOfDayRange_SingleDayMatchesEndOfDay(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	// Local-noon-anchored (ut-docs#869 independent review finding) so the
	// seeded sale reliably lands inside `day` regardless of host timezone —
	// see TestEndOfDay_AggregatesSalesReturnsAndMethods above.
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())
	seedLifecycleSale(t, dbx, "sale1", "R001", "sale", "completed", b8At(today), 220, 20)

	day := b8ExpectedDay(t, dbx.d, today, 0, 0)
	fromEndOfDay, err := dbx.repo.EndOfDay(ctx, day)
	if err != nil {
		t.Fatal(err)
	}
	fromRange, err := dbx.repo.EndOfDayRange(ctx, day, day)
	if err != nil {
		t.Fatal(err)
	}
	if fromRange.SalesCount != fromEndOfDay.SalesCount || fromRange.Gross != fromEndOfDay.Gross {
		t.Fatalf("expected a from==to range to aggregate identically to EndOfDay, got range=%+v day=%+v", fromRange, fromEndOfDay)
	}
}

func TestEndOfDayRange_NoSalesReturnsZeroedReportWithoutError(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	rep, err := dbx.repo.EndOfDayRange(ctx, "2026-06-01", "2026-06-30")
	if err != nil {
		t.Fatal(err)
	}
	if rep.SalesCount != 0 || rep.Gross != 0 || len(rep.Methods) != 0 {
		t.Fatalf("expected a zeroed report for an empty range, got %+v", rep)
	}
}

// The TaxBands golden tests (TestEndOfDay_TaxBands_PerRateNetTaxGross,
// TestEndOfDayRange_TaxBandsAcrossDays) and assertEODTaxBandIdentities
// moved to internal/pages/eod_tax_bands_test.go: rep.TaxBands is no longer
// computed inside dateRangeSummary (the SQL-only aggregation missed the
// service charge's tax and whole-sale discounts — see eod_tax_bands.go),
// so the contract now lives where the computation does.

func TestAuditActionSummary(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	// AuditActionSummary's window is relative to the real wall clock
	// (datetime('now', '-N days')) — use offsets from time.Now(), not
	// hardcoded dates, so the test doesn't silently break as real time
	// moves on.
	relTime := func(d time.Duration) string { return time.Now().Add(d).UTC().Format(time.RFC3339) }

	if err := dbx.repo.InsertAudit(ctx, nil, "user1", "sale", "sale1", "void", nil, relTime(-time.Hour), ""); err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.InsertAudit(ctx, nil, "user1", "sale", "sale2", "void", nil, relTime(-2*time.Hour), ""); err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.InsertAudit(ctx, nil, "user2", "inventory", "itm1", "override", nil, relTime(-3*time.Hour), ""); err != nil {
		t.Fatal(err)
	}
	// Well outside a realistic reporting window — must not be counted once
	// the window is small enough to exclude it.
	if err := dbx.repo.InsertAudit(ctx, nil, "user1", "sale", "sale3", "void", nil, relTime(-60*24*time.Hour), ""); err != nil {
		t.Fatal(err)
	}

	// A wide-but-not-infinite window (30 days back from "now") naturally
	// excludes the 60-day-old entry while including the three recent ones.
	summary, err := dbx.repo.AuditActionSummary(ctx, 30, 10)
	if err != nil {
		t.Fatal(err)
	}
	var user1Voids int
	for _, a := range summary {
		if a.ActorID == "user1" && a.Action == "void" {
			user1Voids = a.Count
		}
	}
	if user1Voids != 2 {
		t.Fatalf("expected 2 voids from user1 within a 30-day window (excluding the 60-day-old entry), got %d in %+v", user1Voids, summary)
	}

	recent, err := dbx.repo.AuditActionSummary(ctx, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range recent {
		if a.ActorID == "user1" && a.Action == "void" && a.Count > 2 {
			t.Fatalf("expected the 60-day-old audit entry excluded from a 1-day window, got %+v", recent)
		}
	}
}
