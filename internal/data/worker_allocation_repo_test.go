package data

import (
	"context"
	"testing"
)

func seedPaymentWithTip(t *testing.T, dbx *posTestDB, id, saleID string, tipAmount int64, tipRecipient, paidAt string) {
	t.Helper()
	ctx := context.Background()
	if _, err := dbx.d.DB.ExecContext(ctx, `
INSERT INTO payment_methods(id, name, type) VALUES('cash', 'Cash', 'cash')
ON CONFLICT(id) DO NOTHING`); err != nil {
		t.Fatalf("seed payment method: %v", err)
	}
	if _, err := dbx.d.DB.ExecContext(ctx, `
INSERT INTO payments(id, sale_id, method_id, amount, currency, change_given, tip_amount, tip_recipient, paid_at)
VALUES(?, ?, 'cash', 1000, 'GBP', 0, ?, ?, ?)`, id, saleID, tipAmount, tipRecipient, paidAt); err != nil {
		t.Fatalf("seed payment %s: %v", id, err)
	}
}

// TestInsertWorkerAllocation confirms a row round-trips: written by
// InsertWorkerAllocation, then readable straight back out of the table.
func TestInsertWorkerAllocation(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	tx, err := dbx.d.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.InsertWorkerAllocation(ctx, tx, "wa1", "tip", "pay1", "user1", 500, "2026-08-25T10:00:00Z", "note here"); err != nil {
		t.Fatalf("InsertWorkerAllocation: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	var sourceType, sourceID, cashierID, allocatedAt, note string
	var amount int64
	err = dbx.d.DB.QueryRowContext(ctx, `SELECT source_type, source_id, cashier_id, amount_minor, allocated_at, note FROM worker_allocations WHERE id = ?`, "wa1").
		Scan(&sourceType, &sourceID, &cashierID, &amount, &allocatedAt, &note)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if sourceType != "tip" || sourceID != "pay1" || cashierID != "user1" || amount != 500 || note != "note here" {
		t.Fatalf("unexpected row: type=%s source=%s cashier=%s amount=%d note=%s", sourceType, sourceID, cashierID, amount, note)
	}
}

// TestWorkerAllocationsArchiveRoundTrip confirms the migration + archive
// twin (ADR-0042 §1) actually round-trips through
// ResetTransactionHistory/RestoreResetBatch: insert, reset (moves to
// worker_allocations_archive), verify the live table is empty and the
// archive holds the row tagged with the batch, then restore and verify the
// row is back.
func TestWorkerAllocationsArchiveRoundTrip(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	tx, err := dbx.d.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.InsertWorkerAllocation(ctx, tx, "wa1", "tip", "pay1", "user1", 500, "2026-08-25T10:00:00Z", ""); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	count, batchID, err := dbx.repo.ResetTransactionHistory(ctx, "user1")
	if err != nil {
		t.Fatalf("ResetTransactionHistory: %v", err)
	}
	_ = count

	var liveCount int
	if err := dbx.d.DB.QueryRowContext(ctx, `SELECT count(*) FROM worker_allocations`).Scan(&liveCount); err != nil {
		t.Fatal(err)
	}
	if liveCount != 0 {
		t.Fatalf("expected live worker_allocations empty after reset, got %d", liveCount)
	}

	var archiveCount int
	if err := dbx.d.DB.QueryRowContext(ctx, `SELECT count(*) FROM worker_allocations_archive WHERE reset_batch_id = ?`, batchID).Scan(&archiveCount); err != nil {
		t.Fatal(err)
	}
	if archiveCount != 1 {
		t.Fatalf("expected 1 archived worker_allocations row, got %d", archiveCount)
	}

	if _, err := dbx.repo.RestoreResetBatch(ctx, batchID, "user1"); err != nil {
		t.Fatalf("RestoreResetBatch: %v", err)
	}
	if err := dbx.d.DB.QueryRowContext(ctx, `SELECT count(*) FROM worker_allocations`).Scan(&liveCount); err != nil {
		t.Fatal(err)
	}
	if liveCount != 1 {
		t.Fatalf("expected 1 worker_allocations row after restore, got %d", liveCount)
	}
}

// TestWorkerAllocationsSummary_Tip proves the "tip" source_type path:
// received reads payments.tip_amount, allocated reads worker_allocations,
// both scoped by date range and cashier — and, per independent review,
// proves the received side actually excludes what it must:
//   - a 'business'-retained tip (ADR-0061 Decision 3) — never owed to a
//     worker, must not appear as "received";
//   - a voided sale's tip — the payment row survives UpdateSaleStatus, so
//     an unfiltered sum would count money that was never taken;
//   - a tip on a date outside the requested range.
func TestWorkerAllocationsSummary_Tip(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	seedLifecycleSale(t, dbx, "sale1", "R1", "sale", "completed", "2026-08-25T09:00:00Z", 1000, 0)
	dbx.d.DB.ExecContext(ctx, `UPDATE sales SET cashier_id = 'user1' WHERE id = 'sale1'`)
	seedPaymentWithTip(t, dbx, "pay1", "sale1", 500, "employee", "2026-08-25T09:05:00Z")

	// Excluded: business-retained tip.
	seedLifecycleSale(t, dbx, "sale2", "R2", "sale", "completed", "2026-08-25T09:00:00Z", 1000, 0)
	dbx.d.DB.ExecContext(ctx, `UPDATE sales SET cashier_id = 'user1' WHERE id = 'sale2'`)
	seedPaymentWithTip(t, dbx, "pay2", "sale2", 9999, "business", "2026-08-25T09:05:00Z")

	// Excluded: voided sale — the payment row is never deleted.
	seedLifecycleSale(t, dbx, "sale3", "R3", "sale", "voided", "2026-08-25T09:00:00Z", 1000, 0)
	dbx.d.DB.ExecContext(ctx, `UPDATE sales SET cashier_id = 'user1' WHERE id = 'sale3'`)
	seedPaymentWithTip(t, dbx, "pay3", "sale3", 8888, "employee", "2026-08-25T09:05:00Z")

	// Excluded: outside the requested date range.
	seedLifecycleSale(t, dbx, "sale4", "R4", "sale", "completed", "2026-01-01T09:00:00Z", 1000, 0)
	dbx.d.DB.ExecContext(ctx, `UPDATE sales SET cashier_id = 'user1' WHERE id = 'sale4'`)
	seedPaymentWithTip(t, dbx, "pay4", "sale4", 7777, "employee", "2026-01-01T09:05:00Z")

	tx, err := dbx.d.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.InsertWorkerAllocation(ctx, tx, "wa1", "tip", "pay1", "user1", 500, "2026-08-25T18:00:00Z", "shift-end payout"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	summary, err := dbx.repo.WorkerAllocationsSummary(ctx, "2026-08-25", "2026-08-25", "user1", "tip")
	if err != nil {
		t.Fatalf("WorkerAllocationsSummary: %v", err)
	}
	if summary.ReceivedMinor != 500 {
		t.Errorf("expected ReceivedMinor 500 (business/voided/out-of-range tips excluded), got %d", summary.ReceivedMinor)
	}
	if summary.AllocatedMinor != 500 {
		t.Errorf("expected AllocatedMinor 500, got %d", summary.AllocatedMinor)
	}
}

// TestWorkerAllocationsSummary_YuzdeUsuluPool proves the "yuzde_usulu_pool"
// path — the same query path as "tip" above, serving a source_type with no
// underlying payments row, per ADR-0063 Decision 3: received matches
// allocated by construction (the ledger is its own evidence until #965's
// own collection-side mechanism lands). Per independent review, also
// proves the source_type and date-range filters actually separate this
// from a same-day 'tip' row and an out-of-range pool row, rather than the
// two source_types happening to pass in isolation.
func TestWorkerAllocationsSummary_YuzdeUsuluPool(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	tx, err := dbx.d.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	// One pool payout batch split between two workers.
	if err := dbx.repo.InsertWorkerAllocation(ctx, tx, "wa1", "yuzde_usulu_pool", "pool-batch-1", "user1", 300, "2026-08-25T20:00:00Z", "kitchen 30%"); err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.InsertWorkerAllocation(ctx, tx, "wa2", "yuzde_usulu_pool", "pool-batch-1", "user2", 700, "2026-08-25T20:00:00Z", "floor 70%"); err != nil {
		t.Fatal(err)
	}
	// A same-day 'tip' allocation — must not leak into the pool's totals.
	if err := dbx.repo.InsertWorkerAllocation(ctx, tx, "wa3", "tip", "pay-other", "user1", 999999, "2026-08-25T20:00:00Z", "unrelated tip"); err != nil {
		t.Fatal(err)
	}
	// A pool allocation outside the requested date range — must be excluded.
	if err := dbx.repo.InsertWorkerAllocation(ctx, tx, "wa4", "yuzde_usulu_pool", "pool-batch-0", "user1", 555555, "2026-01-01T20:00:00Z", "old pool"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	// Unscoped by cashier: whole-pool totals, 'tip' and out-of-range excluded.
	summary, err := dbx.repo.WorkerAllocationsSummary(ctx, "2026-08-25", "2026-08-25", "", "yuzde_usulu_pool")
	if err != nil {
		t.Fatalf("WorkerAllocationsSummary: %v", err)
	}
	if summary.ReceivedMinor != 1000 || summary.AllocatedMinor != 1000 {
		t.Errorf("expected received=allocated=1000 (tip/out-of-range excluded), got received=%d allocated=%d", summary.ReceivedMinor, summary.AllocatedMinor)
	}

	// Scoped to one cashier: allocated narrows, received (whole-pool) does not.
	scoped, err := dbx.repo.WorkerAllocationsSummary(ctx, "2026-08-25", "2026-08-25", "user1", "yuzde_usulu_pool")
	if err != nil {
		t.Fatalf("WorkerAllocationsSummary scoped: %v", err)
	}
	if scoped.AllocatedMinor != 300 {
		t.Errorf("expected AllocatedMinor 300 for user1, got %d", scoped.AllocatedMinor)
	}
	if scoped.ReceivedMinor != 1000 {
		t.Errorf("expected ReceivedMinor 1000 (whole pool), got %d", scoped.ReceivedMinor)
	}
}

// TestWorkerAllocationsSummary_UnknownSourceTypeRejectedAtInsert confirms
// the migration's CHECK constraint (independent review, ut-docs#987) rejects
// a source_type outside the three known values at write time — before this
// fix a typo would write a statutory record silently invisible to every
// report (WorkerAllocationsSummary's own default case already rejects it
// on read, but nothing stopped it being written in the first place).
func TestWorkerAllocationsSummary_UnknownSourceTypeRejectedAtInsert(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	tx, err := dbx.d.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := dbx.repo.InsertWorkerAllocation(ctx, tx, "wa1", "tips", "pay1", "user1", 500, "2026-08-25T10:00:00Z", ""); err == nil {
		t.Fatal("expected the CHECK constraint to reject an unknown source_type, got nil error")
	}
}

// TestWorkerAllocationsSummary_UnsupportedSourceType confirms the summary
// query fails closed on an unknown source_type rather than silently
// returning zeroed totals.
func TestWorkerAllocationsSummary_UnsupportedSourceType(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	if _, err := dbx.repo.WorkerAllocationsSummary(ctx, "2026-08-25", "2026-08-25", "", "not_a_real_type"); err == nil {
		t.Fatal("expected an error for an unsupported source_type, got nil")
	}
}

// TestListWorkerAllocations_RoundTrip confirms a written row comes back out
// with every field intact (id/source_type/source_id/cashier_id/amount_minor/
// allocated_at/note) — the row-level detail behind WorkerAllocationsSummary's
// AllocatedMinor total.
func TestListWorkerAllocations_RoundTrip(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	tx, err := dbx.d.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.InsertWorkerAllocation(ctx, tx, "wa1", "tip", "pay1", "user1", 500, "2026-08-25T18:00:00Z", "shift-end payout"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	rows, err := dbx.repo.ListWorkerAllocations(ctx, "2026-08-25", "2026-08-25", "", "tip")
	if err != nil {
		t.Fatalf("ListWorkerAllocations: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	got := rows[0]
	if got.ID != "wa1" || got.SourceType != "tip" || got.SourceID != "pay1" || got.CashierID != "user1" ||
		got.AmountMinor != 500 || got.AllocatedAt != "2026-08-25T18:00:00Z" || got.Note != "shift-end payout" {
		t.Fatalf("unexpected row: %+v", got)
	}
}

// TestListWorkerAllocations_DateRangeFiltering confirms only rows whose
// allocated_at falls within [from, to] (local calendar day, inclusive) come
// back — same convention WorkerAllocationsSummary uses.
func TestListWorkerAllocations_DateRangeFiltering(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	tx, err := dbx.d.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.InsertWorkerAllocation(ctx, tx, "wa1", "tip", "pay1", "user1", 500, "2026-08-25T18:00:00Z", "in range"); err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.InsertWorkerAllocation(ctx, tx, "wa2", "tip", "pay2", "user1", 999, "2026-01-01T18:00:00Z", "out of range"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	rows, err := dbx.repo.ListWorkerAllocations(ctx, "2026-08-25", "2026-08-25", "", "tip")
	if err != nil {
		t.Fatalf("ListWorkerAllocations: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != "wa1" {
		t.Fatalf("expected only wa1 in range, got %+v", rows)
	}
}

// TestListWorkerAllocations_CashierFiltering confirms cashierID narrows the
// result to that worker's own payouts only, "" returns every worker's.
func TestListWorkerAllocations_CashierFiltering(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	tx, err := dbx.d.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.InsertWorkerAllocation(ctx, tx, "wa1", "tip", "pay1", "user1", 500, "2026-08-25T18:00:00Z", ""); err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.InsertWorkerAllocation(ctx, tx, "wa2", "tip", "pay2", "user2", 700, "2026-08-25T18:05:00Z", ""); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	scoped, err := dbx.repo.ListWorkerAllocations(ctx, "2026-08-25", "2026-08-25", "user1", "tip")
	if err != nil {
		t.Fatalf("ListWorkerAllocations scoped: %v", err)
	}
	if len(scoped) != 1 || scoped[0].CashierID != "user1" {
		t.Fatalf("expected only user1's row, got %+v", scoped)
	}

	all, err := dbx.repo.ListWorkerAllocations(ctx, "2026-08-25", "2026-08-25", "", "tip")
	if err != nil {
		t.Fatalf("ListWorkerAllocations all: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected both rows unscoped, got %d", len(all))
	}
}

// TestListWorkerAllocations_OrderedByAllocatedAtDesc confirms the result is
// most-recent-first.
func TestListWorkerAllocations_OrderedByAllocatedAtDesc(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	tx, err := dbx.d.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.InsertWorkerAllocation(ctx, tx, "wa-early", "tip", "pay1", "user1", 100, "2026-08-25T09:00:00Z", ""); err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.InsertWorkerAllocation(ctx, tx, "wa-late", "tip", "pay2", "user1", 200, "2026-08-25T20:00:00Z", ""); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	rows, err := dbx.repo.ListWorkerAllocations(ctx, "2026-08-25", "2026-08-25", "", "tip")
	if err != nil {
		t.Fatalf("ListWorkerAllocations: %v", err)
	}
	if len(rows) != 2 || rows[0].ID != "wa-late" || rows[1].ID != "wa-early" {
		t.Fatalf("expected [wa-late, wa-early] order, got %+v", rows)
	}
}

// TestListWorkerAllocations_SourceTypeRequired mirrors
// WorkerAllocationsSummary's own guard: "" is rejected rather than silently
// listing every source_type's rows through one report.
func TestListWorkerAllocations_SourceTypeRequired(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	if _, err := dbx.repo.ListWorkerAllocations(ctx, "2026-08-25", "2026-08-25", "", ""); err == nil {
		t.Fatal("expected an error for an empty source_type, got nil")
	}
}

// TestListWorkerAllocations_UnsupportedSourceType mirrors
// WorkerAllocationsSummary's own default case.
func TestListWorkerAllocations_UnsupportedSourceType(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	if _, err := dbx.repo.ListWorkerAllocations(ctx, "2026-08-25", "2026-08-25", "", "not_a_real_type"); err == nil {
		t.Fatal("expected an error for an unsupported source_type, got nil")
	}
}
