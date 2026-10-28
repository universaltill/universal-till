package data

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func seedRestoreSale(t *testing.T, dbx *posTestDB, receiptNo string, at time.Time) {
	t.Helper()
	if _, err := dbx.d.DB.Exec(`INSERT INTO sales (id, receipt_no, subtotal, total, created_at) VALUES (?, ?, 100, 100, ?)`,
		"s-"+receiptNo, receiptNo, at.UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
}

func seedRestoreZ(t *testing.T, dbx *posTestDB, kind string, z int64, at time.Time) {
	t.Helper()
	if _, err := dbx.d.DB.Exec(`INSERT INTO report_archive (id, kind, period, content_json, z_number, created_at) VALUES (?, ?, ?, '{}', ?, ?)`,
		fmt.Sprintf("ra-%s-%d", kind, z), kind, fmt.Sprintf("p-%s-%d", kind, z), z, at.UTC().Format(archiveTimestampFmt)); err != nil {
		t.Fatal(err)
	}
}

func setRestoreSetting(t *testing.T, dbx *posTestDB, k, v string) {
	t.Helper()
	if _, err := dbx.d.DB.Exec(`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, k, v); err != nil {
		t.Fatal(err)
	}
}

func mustNextReceipt(t *testing.T, dbx *posTestDB) string {
	t.Helper()
	n, err := dbx.repo.NextReceiptNo(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestJumpNumbersAfterRestore_ExactFloorsAndContinuation(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	// Three days, five sales each (peak 5), last activity 2 days before now.
	n := 0
	for d := 4; d >= 2; d-- {
		for i := 0; i < 5; i++ {
			n++
			seedRestoreSale(t, dbx, fmt.Sprintf("%09d", n), now.AddDate(0, 0, -d).Add(time.Duration(i)*time.Minute))
		}
	}
	seedRestoreZ(t, dbx, "eod", 7, now.AddDate(0, 0, -4))
	seedRestoreZ(t, dbx, "eod", 8, now.AddDate(0, 0, -3))
	seedRestoreZ(t, dbx, "weekly", 3, now.AddDate(0, 0, -3))
	// Latest activity: a Z exactly 2 days before now (sales end earlier).
	seedRestoreZ(t, dbx, "eod", 9, now.AddDate(0, 0, -2).Add(time.Hour))

	j, err := dbx.repo.JumpNumbersAfterRestore(ctx, now, "")
	if err != nil {
		t.Fatal(err)
	}
	// lastActivity = now-2d+1h -> 1d23h before now -> ceil = 2, +1 = 3 days.
	if j.Days != 3 || j.PeakPerDay != 5 {
		t.Fatalf("days/peak = %d/%d, want 3/5", j.Days, j.PeakPerDay)
	}
	// jump = max(10000, 2*5*3) = 10000; 15+10000 = 10015 -> 11000.
	if j.ReceiptFloor != 11000 {
		t.Fatalf("receipt floor = %d, want 11000", j.ReceiptFloor)
	}
	if j.ZFloor["eod"] != 9+3 || j.ZFloor["weekly"] != 3+3 {
		t.Fatalf("z floors = %v, want eod 12 weekly 6", j.ZFloor)
	}
	m := j.Marker
	if m.LastReceipt != "000000015" || m.NextReceipt != "000011001" || m.LastZ != 9 || m.NextZ != 13 {
		t.Fatalf("marker = %+v", m)
	}

	if got := mustNextReceipt(t, dbx); got != "000011001" {
		t.Fatalf("next receipt = %q, want 000011001", got)
	}
	seedRestoreSale(t, dbx, "000011001", now)
	if got := mustNextReceipt(t, dbx); got != "000011002" {
		t.Fatalf("second next receipt = %q, want 000011002", got)
	}

	// The closedAt statement variant runs FIRST on the fresh floor, so
	// reverting only that variant's floor handling fails here.
	closedAt := now.Add(24 * time.Hour)
	if created, err := dbx.repo.ArchiveReport(ctx, "eod", closedAt.Format(time.RFC3339), []byte(`{}`), "", "", closedAt); err != nil || !created {
		t.Fatalf("archive (closedAt): %v created=%v", err, created)
	}
	row := findArchived(t, dbx.repo, "eod", closedAt.Format(time.RFC3339))
	if row.ZNumber != 13 || row.PrevZNumber == nil || *row.PrevZNumber != 9 {
		t.Fatalf("closedAt variant: z=%d prev=%v, want 13 / 9", row.ZNumber, row.PrevZNumber)
	}
	// The plain variant, on a kind whose floor is still fresh.
	if _, err := dbx.repo.ArchiveReport(ctx, "weekly", "2026-W43", []byte(`{}`), "", "", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if r := findArchived(t, dbx.repo, "weekly", "2026-W43"); r.ZNumber != 7 || r.PrevZNumber == nil || *r.PrevZNumber != 3 {
		t.Fatalf("plain variant: z=%d prev=%v, want 7 / 3", r.ZNumber, r.PrevZNumber)
	}
	if created, err := dbx.repo.ArchiveReport(ctx, "eod", "2026-10-20", []byte(`{}`), "", "", time.Time{}); err != nil || !created {
		t.Fatalf("archive: %v created=%v", err, created)
	}
	row = findArchived(t, dbx.repo, "eod", "2026-10-20")
	if row.ZNumber != 14 || row.PrevZNumber == nil || *row.PrevZNumber != 13 {
		t.Fatalf("z=%d prev=%v, want 14 / 13", row.ZNumber, row.PrevZNumber)
	}
	// A kind without a floor is untouched.
	if _, err := dbx.repo.ArchiveReport(ctx, "monthly", "2026-10", []byte(`{}`), "", "", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if r := findArchived(t, dbx.repo, "monthly", "2026-10"); r.ZNumber != 1 {
		t.Fatalf("monthly z = %d, want 1", r.ZNumber)
	}
}

func TestRestoreJumpSize(t *testing.T) {
	if got := restoreJumpSize(5, 3); got != 10000 {
		t.Fatalf("small = %d", got)
	}
	if got := restoreJumpSize(2000, 4); got != 16000 {
		t.Fatalf("big = %d, want 16000", got)
	}
	if got := roundUpTo(16015, 1000); got != 17000 {
		t.Fatalf("round = %d", got)
	}
	if got := roundUpTo(17000, 1000); got != 17000 {
		t.Fatalf("round exact = %d", got)
	}
}

func TestNextReceiptNo_FloorForOtherPrefixIgnored(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	setRestoreSetting(t, dbx, "sync.receipt_prefix", "T2-")
	setRestoreSetting(t, dbx, RestoreReceiptFloorKey, "50000")
	setRestoreSetting(t, dbx, RestoreReceiptFloorPrefixKey, "T3-")
	seedRestoreSale(t, dbx, "T2-000000004", time.Now())
	if got := mustNextReceipt(t, dbx); got != "T2-000000005" {
		t.Fatalf("next = %q, want T2-000000005", got)
	}
	setRestoreSetting(t, dbx, RestoreReceiptFloorPrefixKey, "T2-")
	if got := mustNextReceipt(t, dbx); got != "T2-000050001" {
		t.Fatalf("next = %q, want T2-000050001", got)
	}
}

func TestNextReceiptNo_FloorBelowLiveMaxDoesNothing(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	setRestoreSetting(t, dbx, RestoreReceiptFloorKey, "100")
	setRestoreSetting(t, dbx, RestoreReceiptFloorPrefixKey, "")
	seedRestoreSale(t, dbx, "000000500", time.Now())
	if got := mustNextReceipt(t, dbx); got != "000000501" {
		t.Fatalf("next = %q, want 000000501", got)
	}
}

func TestArchiveReport_ZFloorBelowMaxDoesNothing(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	setRestoreSetting(t, dbx, RestoreZFloorKeyPrefix+"eod", "2")
	seedRestoreZ(t, dbx, "eod", 5, time.Now())
	if _, err := dbx.repo.ArchiveReport(context.Background(), "eod", "next", []byte(`{}`), "", "", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if r := findArchived(t, dbx.repo, "eod", "next"); r.ZNumber != 6 {
		t.Fatalf("z = %d, want 6", r.ZNumber)
	}
}

func TestJumpNumbersAfterRestore_TwiceKeepsGoingUp(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	seedRestoreSale(t, dbx, "000000010", now.Add(-time.Hour))
	seedRestoreZ(t, dbx, "eod", 4, now.Add(-time.Hour))
	first, err := dbx.repo.JumpNumbersAfterRestore(ctx, now, "")
	if err != nil {
		t.Fatal(err)
	}
	// Restored again from the same backup: the settings table (floors) is
	// whatever the second restore carries; here the floors persist, as when
	// the till restores a newer backup that already contained them.
	second, err := dbx.repo.JumpNumbersAfterRestore(ctx, now, "")
	if err != nil {
		t.Fatal(err)
	}
	if second.ReceiptFloor <= first.ReceiptFloor {
		t.Fatalf("receipt floor %d not above %d", second.ReceiptFloor, first.ReceiptFloor)
	}
	// 11000 (first floor 10010 -> 11000) + 10000 = 21000.
	if first.ReceiptFloor != 11000 || second.ReceiptFloor != 21000 {
		t.Fatalf("floors %d, %d want 11000, 21000", first.ReceiptFloor, second.ReceiptFloor)
	}
	if second.ZFloor["eod"] <= first.ZFloor["eod"] {
		t.Fatalf("z floor %d not above %d", second.ZFloor["eod"], first.ZFloor["eod"])
	}
	if second.Marker.LastReceipt != "000011000" {
		t.Fatalf("last receipt = %q", second.Marker.LastReceipt)
	}
}

func TestJumpNumbersAfterRestore_EmptyDB(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	j, err := dbx.repo.JumpNumbersAfterRestore(ctx, time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC), "")
	if err != nil {
		t.Fatal(err)
	}
	if j.Days != 1 || j.ReceiptFloor != 10000 || j.ZFloor["eod"] != 1 {
		t.Fatalf("jump = %+v", j)
	}
	if j.Marker.LastReceipt != "" || j.Marker.LastActivityAt != "" || j.Marker.NextReceipt != "000010001" || j.Marker.NextZ != 2 {
		t.Fatalf("marker = %+v", j.Marker)
	}
	if got := mustNextReceipt(t, dbx); got != "000010001" {
		t.Fatalf("next = %q", got)
	}
}

func TestRestoreMarker_PendingAndClear(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	if m, err := dbx.repo.PendingRestoreMarker(ctx); err != nil || m != nil {
		t.Fatalf("want none, got %v %v", m, err)
	}
	j, err := dbx.repo.JumpNumbersAfterRestore(ctx, time.Now(), "")
	if err != nil {
		t.Fatal(err)
	}
	m, err := dbx.repo.PendingRestoreMarker(ctx)
	if err != nil || m == nil || *m != j.Marker {
		t.Fatalf("pending = %+v err=%v, want %+v", m, err, j.Marker)
	}
	if err := dbx.repo.ClearRestoreMarker(ctx); err != nil {
		t.Fatal(err)
	}
	if m, err := dbx.repo.PendingRestoreMarker(ctx); err != nil || m != nil {
		t.Fatalf("after clear: %v %v", m, err)
	}
	// Floors survive the clear.
	if got := mustNextReceipt(t, dbx); got != "000010001" {
		t.Fatalf("next = %q", got)
	}
}

func countRestoreAudits(t *testing.T, dbx *posTestDB) int {
	t.Helper()
	var n int
	if err := dbx.d.DB.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'restore_numbers_jumped'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestJumpNumbersAfterRestore_SameTokenIsIdempotentNewTokenJumpsAgain(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	seedRestoreSale(t, dbx, "000000010", now.Add(-time.Hour))
	seedRestoreZ(t, dbx, "eod", 4, now.Add(-time.Hour))

	first, err := dbx.repo.JumpNumbersAfterRestore(ctx, now, "tok-1")
	if err != nil || first.AlreadyApplied {
		t.Fatalf("first: %+v %v", first, err)
	}
	again, err := dbx.repo.JumpNumbersAfterRestore(ctx, now, "tok-1")
	if err != nil || !again.AlreadyApplied {
		t.Fatalf("same token: %+v %v", again, err)
	}
	if got := mustNextReceipt(t, dbx); got != "000011001" {
		t.Fatalf("next receipt after repeat = %q, want 000011001", got)
	}
	m, _ := dbx.repo.PendingRestoreMarker(ctx)
	if m == nil || *m != first.Marker {
		t.Fatalf("marker changed by the repeat: %+v", m)
	}
	if n := countRestoreAudits(t, dbx); n != 1 {
		t.Fatalf("audit rows = %d, want 1", n)
	}

	second, err := dbx.repo.JumpNumbersAfterRestore(ctx, now, "tok-2")
	if err != nil || second.AlreadyApplied || second.ReceiptFloor <= first.ReceiptFloor {
		t.Fatalf("new token: %+v %v", second, err)
	}
	if n := countRestoreAudits(t, dbx); n != 2 {
		t.Fatalf("audit rows = %d, want 2", n)
	}
}
