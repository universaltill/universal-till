package data

import (
	"context"
	"testing"
)

// TestInsertYuzdeUsuluPoolCollection confirms a row round-trips: written by
// InsertYuzdeUsuluPoolCollection, then readable straight back out of the
// table — including the local_date the insert precomputes (ut-docs#869/
// #1342), since every date-range read of this table matches on that column
// and nothing else would notice if it were written wrong.
func TestInsertYuzdeUsuluPoolCollection(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	tx, err := dbx.d.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.InsertYuzdeUsuluPoolCollection(ctx, tx, "pc1", "mgr1", 4500, "2026-08-25T10:00:00Z", "kitchen 30% / floor 70%"); err != nil {
		t.Fatalf("InsertYuzdeUsuluPoolCollection: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	var recordedBy, collectedAt, basisNote, localDate string
	var amount int64
	err = dbx.d.DB.QueryRowContext(ctx, `
SELECT recorded_by, amount_minor, collected_at, basis_note, local_date
FROM yuzde_usulu_pool_collections WHERE id = ?`, "pc1").
		Scan(&recordedBy, &amount, &collectedAt, &basisNote, &localDate)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if recordedBy != "mgr1" || amount != 4500 || collectedAt != "2026-08-25T10:00:00Z" || basisNote != "kitchen 30% / floor 70%" {
		t.Fatalf("unexpected row: recorded_by=%s amount=%d collected_at=%s basis=%s", recordedBy, amount, collectedAt, basisNote)
	}
	// Derived via SQLite's own date(...,'localtime') rather than a
	// hardcoded Go-side literal (ut-docs#1869's convention in this package):
	// the expected value depends on the host TZ this suite runs under.
	wantLocalDate := b8ExpectedDay(t, dbx.d, mustParseRFC3339(t, "2026-08-25T10:00:00Z"), 0, 0)
	if localDate != wantLocalDate {
		t.Fatalf("local_date = %q, want %q (precomputed at insert time)", localDate, wantLocalDate)
	}
}

// TestListYuzdeUsuluPoolCollections_RoundTripAndOrdering confirms every
// field comes back intact, that the local_date range filter actually
// excludes an out-of-range collection, and that rows arrive most-recent-
// first (collected_at DESC) — the order the tab's pool picker and
// collections table depend on.
func TestListYuzdeUsuluPoolCollections_RoundTripAndOrdering(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	tx, err := dbx.d.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.InsertYuzdeUsuluPoolCollection(ctx, tx, "pc1", "mgr1", 4500, "2026-08-25T10:00:00Z", "lunch pool"); err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.InsertYuzdeUsuluPoolCollection(ctx, tx, "pc2", "mgr1", 2500, "2026-08-25T13:00:00Z", "dinner pool"); err != nil {
		t.Fatal(err)
	}
	// Outside the requested range — must be excluded.
	if err := dbx.repo.InsertYuzdeUsuluPoolCollection(ctx, tx, "pc3", "mgr1", 999999, "2026-01-01T10:00:00Z", "old pool"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	day := b8ExpectedDay(t, dbx.d, mustParseRFC3339(t, "2026-08-25T10:00:00Z"), 0, 0)
	rows, err := dbx.repo.ListYuzdeUsuluPoolCollections(ctx, day, day)
	if err != nil {
		t.Fatalf("ListYuzdeUsuluPoolCollections: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 in-range collections (the January one excluded), got %d: %+v", len(rows), rows)
	}
	if rows[0].ID != "pc2" || rows[1].ID != "pc1" {
		t.Fatalf("expected collected_at DESC ordering (pc2 then pc1), got %s then %s", rows[0].ID, rows[1].ID)
	}
	if rows[0].AmountMinor != 2500 || rows[0].BasisNote != "dinner pool" || rows[0].RecordedBy != "mgr1" || rows[0].CollectedAt != "2026-08-25T13:00:00Z" {
		t.Fatalf("unexpected first row: %+v", rows[0])
	}
}

// TestYuzdeUsuluPoolCollectionsTotal confirms the sum honors the same
// local_date range filter as the list query, and that an empty period is a
// plain zero (COALESCE) rather than an error or a NULL scan failure.
func TestYuzdeUsuluPoolCollectionsTotal(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	day := b8ExpectedDay(t, dbx.d, mustParseRFC3339(t, "2026-08-25T10:00:00Z"), 0, 0)
	total, err := dbx.repo.YuzdeUsuluPoolCollectionsTotal(ctx, day, day)
	if err != nil {
		t.Fatalf("YuzdeUsuluPoolCollectionsTotal (empty): %v", err)
	}
	if total != 0 {
		t.Fatalf("expected 0 for a period with no collections, got %d", total)
	}

	tx, err := dbx.d.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.InsertYuzdeUsuluPoolCollection(ctx, tx, "pc1", "mgr1", 4500, "2026-08-25T10:00:00Z", ""); err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.InsertYuzdeUsuluPoolCollection(ctx, tx, "pc2", "mgr1", 2500, "2026-08-25T13:00:00Z", ""); err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.InsertYuzdeUsuluPoolCollection(ctx, tx, "pc3", "mgr1", 999999, "2026-01-01T10:00:00Z", ""); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	total, err = dbx.repo.YuzdeUsuluPoolCollectionsTotal(ctx, day, day)
	if err != nil {
		t.Fatalf("YuzdeUsuluPoolCollectionsTotal: %v", err)
	}
	if total != 7000 {
		t.Fatalf("expected 7000 (4500+2500, January's 999999 out of range), got %d", total)
	}
}

// TestGetYuzdeUsuluPoolCollection confirms the found-bool convention (the
// same one AuthRepo.GetUser uses): a real id returns the row with found
// true, an unknown id returns found false and NO error — so the API can
// tell "no such pool" (a 400 on an operator-supplied pool_id) apart from a
// query failure (a 500).
func TestGetYuzdeUsuluPoolCollection(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	tx, err := dbx.d.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.InsertYuzdeUsuluPoolCollection(ctx, tx, "pc1", "mgr1", 4500, "2026-08-25T10:00:00Z", "kitchen 30% / floor 70%"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	got, found, err := dbx.repo.GetYuzdeUsuluPoolCollection(ctx, "pc1")
	if err != nil {
		t.Fatalf("GetYuzdeUsuluPoolCollection: %v", err)
	}
	if !found {
		t.Fatal("expected found=true for a real collection id")
	}
	if got.ID != "pc1" || got.AmountMinor != 4500 || got.RecordedBy != "mgr1" || got.BasisNote != "kitchen 30% / floor 70%" || got.CollectedAt != "2026-08-25T10:00:00Z" {
		t.Fatalf("unexpected row: %+v", got)
	}

	_, found, err = dbx.repo.GetYuzdeUsuluPoolCollection(ctx, "nope")
	if err != nil {
		t.Fatalf("expected no error for an unknown id, got %v", err)
	}
	if found {
		t.Fatal("expected found=false for an unknown collection id")
	}
}

// TestYuzdeUsuluPoolCollectionsArchiveRoundTrip confirms migration 037's
// archive twin (ADR-0042 §1) actually round-trips through
// ResetTransactionHistory/RestoreResetBatch, exactly as
// TestWorkerAllocationsArchiveRoundTrip does for the distribution side:
// insert, reset (row moves to yuzde_usulu_pool_collections_archive tagged
// with the batch, live table empty), then restore and verify the row — and
// its local_date, which every date-range read depends on — is back.
func TestYuzdeUsuluPoolCollectionsArchiveRoundTrip(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	tx, err := dbx.d.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.InsertYuzdeUsuluPoolCollection(ctx, tx, "pc1", "user1", 4500, "2026-08-25T10:00:00Z", "kitchen 30% / floor 70%"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	wantLocalDate := b8ExpectedDay(t, dbx.d, mustParseRFC3339(t, "2026-08-25T10:00:00Z"), 0, 0)

	_, batchID, err := dbx.repo.ResetTransactionHistory(ctx, "user1", "")
	if err != nil {
		t.Fatalf("ResetTransactionHistory: %v", err)
	}

	var liveCount int
	if err := dbx.d.DB.QueryRowContext(ctx, `SELECT count(*) FROM yuzde_usulu_pool_collections`).Scan(&liveCount); err != nil {
		t.Fatal(err)
	}
	if liveCount != 0 {
		t.Fatalf("expected the live yuzde_usulu_pool_collections table empty after reset, got %d", liveCount)
	}

	var archivedAmount int64
	var archivedBasis, archivedLocalDate string
	err = dbx.d.DB.QueryRowContext(ctx, `
SELECT amount_minor, basis_note, local_date FROM yuzde_usulu_pool_collections_archive
WHERE id = 'pc1' AND reset_batch_id = ?`, batchID).Scan(&archivedAmount, &archivedBasis, &archivedLocalDate)
	if err != nil {
		t.Fatalf("read archived collection: %v", err)
	}
	if archivedAmount != 4500 || archivedBasis != "kitchen 30% / floor 70%" || archivedLocalDate != wantLocalDate {
		t.Fatalf("unexpected archived row: amount=%d basis=%q local_date=%q (want local_date %q)", archivedAmount, archivedBasis, archivedLocalDate, wantLocalDate)
	}

	if _, err := dbx.repo.RestoreResetBatch(ctx, batchID, "user1", ""); err != nil {
		t.Fatalf("RestoreResetBatch: %v", err)
	}

	var restoredAmount int64
	var restoredLocalDate string
	if err := dbx.d.DB.QueryRowContext(ctx, `SELECT amount_minor, local_date FROM yuzde_usulu_pool_collections WHERE id = 'pc1'`).
		Scan(&restoredAmount, &restoredLocalDate); err != nil {
		t.Fatalf("read restored collection: %v", err)
	}
	if restoredAmount != 4500 || restoredLocalDate != wantLocalDate {
		t.Fatalf("restored row: amount=%d local_date=%q, want 4500/%q", restoredAmount, restoredLocalDate, wantLocalDate)
	}
	var archiveLeft int
	if err := dbx.d.DB.QueryRowContext(ctx, `SELECT count(*) FROM yuzde_usulu_pool_collections_archive WHERE reset_batch_id = ?`, batchID).Scan(&archiveLeft); err != nil {
		t.Fatal(err)
	}
	if archiveLeft != 0 {
		t.Fatalf("expected the archive cleared after restore, got %d row(s)", archiveLeft)
	}
}

// TestWorkerAllocationsSummary_YuzdeUsuluPoolReceivedCanDifferFromAllocated
// is the exact case that was IMPOSSIBLE to express before ut-docs#988: the
// old "received" query summed the same worker_allocations rows as
// "allocated", so the two could never disagree. With the independent
// collection record, a pool collected but only partly distributed reads
// Received 1000 / Allocated 600 — the shortfall this report exists to make
// visible.
func TestWorkerAllocationsSummary_YuzdeUsuluPoolReceivedCanDifferFromAllocated(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	tx, err := dbx.d.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.InsertYuzdeUsuluPoolCollection(ctx, tx, "pc1", "mgr1", 1000, "2026-08-25T10:00:00Z", "kitchen 30% / floor 70%"); err != nil {
		t.Fatal(err)
	}
	// Only part of the pool has been distributed so far.
	if err := dbx.repo.InsertWorkerAllocation(ctx, tx, "wa1", "yuzde_usulu_pool", "pc1", "user1", 600, "2026-08-25T11:00:00Z", "floor share"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	day := b8ExpectedDay(t, dbx.d, mustParseRFC3339(t, "2026-08-25T10:00:00Z"), 0, 0)
	summary, err := dbx.repo.WorkerAllocationsSummary(ctx, day, day, "", "yuzde_usulu_pool")
	if err != nil {
		t.Fatalf("WorkerAllocationsSummary: %v", err)
	}
	if summary.ReceivedMinor != 1000 {
		t.Errorf("expected ReceivedMinor 1000 (from the collection record), got %d", summary.ReceivedMinor)
	}
	if summary.AllocatedMinor != 600 {
		t.Errorf("expected AllocatedMinor 600 (only part distributed), got %d", summary.AllocatedMinor)
	}
}

// A pool DISTRIBUTION row must never be counted as a COLLECTION: with no
// yuzde_usulu_pool_collections row at all, Received is 0 even though
// worker_allocations holds pool rows. This is the tautology's own
// regression test — if the "received" branch ever goes back to summing
// worker_allocations, this fails.
func TestWorkerAllocationsSummary_YuzdeUsuluPoolReceivedIgnoresAllocations(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	tx, err := dbx.d.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbx.repo.InsertWorkerAllocation(ctx, tx, "wa1", "yuzde_usulu_pool", "pool-batch-1", "user1", 300, "2026-08-25T10:00:00Z", "kitchen 30%"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	day := b8ExpectedDay(t, dbx.d, mustParseRFC3339(t, "2026-08-25T10:00:00Z"), 0, 0)
	summary, err := dbx.repo.WorkerAllocationsSummary(ctx, day, day, "", "yuzde_usulu_pool")
	if err != nil {
		t.Fatalf("WorkerAllocationsSummary: %v", err)
	}
	if summary.ReceivedMinor != 0 {
		t.Errorf("expected ReceivedMinor 0 with no collection record (allocations must not count as received), got %d", summary.ReceivedMinor)
	}
	if summary.AllocatedMinor != 300 {
		t.Errorf("expected AllocatedMinor 300, got %d", summary.AllocatedMinor)
	}
}
