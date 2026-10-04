package data

import (
	"context"
	"testing"
	"time"
)

// ADR-0147 (ut-docs#574, ADR-0040 card 4): the till side of the report
// archive cloud upload — the un-acked queue, the ack, and the three prune
// predicates, each of which must keep the newest row and the highest-Z row
// of every kind so the Z chain survives a prune.

// seedClose archives one eod close at closedAt and returns its row id.
func seedClose(t *testing.T, dbx *posTestDB, closedAt time.Time) string {
	t.Helper()
	ctx := context.Background()
	period := closedAt.Format(time.RFC3339)
	created, err := dbx.repo.ArchiveReport(ctx, "eod", period, []byte(`{"period":"`+period+`"}`), "", "", closedAt)
	if err != nil || !created {
		t.Fatalf("archive close %s: created=%v err=%v", period, created, err)
	}
	row, ok, err := dbx.repo.GetArchivedReport(ctx, "eod", period)
	if err != nil || !ok {
		t.Fatalf("read back close %s: ok=%v err=%v", period, ok, err)
	}
	return row.ID
}

func ackAll(t *testing.T, dbx *posTestDB) {
	t.Helper()
	if _, err := dbx.d.DB.Exec(`UPDATE report_archive SET cloud_acked_at = '2026-10-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
}

func archiveIDs(t *testing.T, dbx *posTestDB) map[string]bool {
	t.Helper()
	rows, err := dbx.d.DB.Query(`SELECT id FROM report_archive`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		out[id] = true
	}
	return out
}

func threeCloses(t *testing.T, dbx *posTestDB) (base time.Time, ids []string) {
	t.Helper()
	base = time.Date(2026, 9, 1, 21, 0, 0, 0, time.Local)
	for i := range 3 {
		ids = append(ids, seedClose(t, dbx, base.AddDate(0, 0, i)))
	}
	return base, ids
}

func TestUnackedReportArchives_ListCountAndAck(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	_, ids := threeCloses(t, dbx)

	n, err := dbx.repo.CountUnackedReportArchives(ctx)
	if err != nil || n != 3 {
		t.Fatalf("count = %d err=%v, want 3", n, err)
	}
	got, err := dbx.repo.ListUnackedReportArchives(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != ids[0] || got[1].ID != ids[1] {
		t.Fatalf("list(2) = %+v, want the two oldest closes in order", got)
	}
	if got[0].Kind != "eod" || got[0].Period == "" || got[0].Content == "" {
		t.Fatalf("list row missing fields: %+v", got[0])
	}

	ok, err := dbx.repo.MarkReportArchiveAcked(ctx, ids[0], time.Now())
	if err != nil || !ok {
		t.Fatalf("first ack ok=%v err=%v", ok, err)
	}
	ok, err = dbx.repo.MarkReportArchiveAcked(ctx, ids[0], time.Now().Add(time.Hour))
	if err != nil || ok {
		t.Fatalf("second ack of the same row ok=%v err=%v, want false (already acked)", ok, err)
	}
	if n, _ := dbx.repo.CountUnackedReportArchives(ctx); n != 2 {
		t.Fatalf("count after ack = %d, want 2", n)
	}
	got, _ = dbx.repo.ListUnackedReportArchives(ctx, 50)
	if len(got) != 2 || got[0].ID != ids[1] {
		t.Fatalf("list after ack = %+v, want the acked row gone", got)
	}
}

// The ADR-0147 §4 scenario: mode cloud, every row acked. The prune must keep
// the newest eod row so the next close still numbers Z = previous max + 1,
// links prev_closed_at to the last close, and LatestArchivedAt still knows
// when the last close was.
func TestPruneReportArchiveAcked_KeepsNewestRowAndZChainContinues(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	base, ids := threeCloses(t, dbx)
	ackAll(t, dbx)

	n, err := dbx.repo.PruneReportArchiveAcked(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("pruned %d, want 2 (the newest eod row is kept)", n)
	}
	left := archiveIDs(t, dbx)
	if len(left) != 1 || !left[ids[2]] {
		t.Fatalf("rows left = %v, want only the newest close %s", left, ids[2])
	}
	last := base.AddDate(0, 0, 2)
	latest, err := dbx.repo.LatestArchivedAt(ctx, "eod")
	if err != nil || latest == nil || !latest.Equal(last.UTC()) {
		t.Fatalf("LatestArchivedAt = %v err=%v, want %v", latest, err, last.UTC())
	}

	// Idempotent: the kept row stays kept.
	if n, err := dbx.repo.PruneReportArchiveAcked(ctx); err != nil || n != 0 {
		t.Fatalf("second prune n=%d err=%v, want 0", n, err)
	}

	next := base.AddDate(0, 0, 3)
	seedClose(t, dbx, next)
	row, ok, err := dbx.repo.GetArchivedReport(ctx, "eod", next.Format(time.RFC3339))
	if err != nil || !ok {
		t.Fatal(err)
	}
	if row.ZNumber != 4 {
		t.Fatalf("next close z_number = %d, want 4 (previous max + 1)", row.ZNumber)
	}
	if row.PrevZNumber == nil || *row.PrevZNumber != 3 {
		t.Fatalf("prev_z_number = %v, want 3", row.PrevZNumber)
	}
	if row.PrevClosedAt == nil || *row.PrevClosedAt != last.UTC().Format(time.RFC3339) {
		t.Fatalf("prev_closed_at = %v, want %s", row.PrevClosedAt, last.UTC().Format(time.RFC3339))
	}
}

// The newest created_at and the highest z_number can be different rows (a
// row's created_at moved, or a legacy un-numbered row); both are kept.
func TestPruneReportArchive_KeepsBothNewestAndHighestZRows(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	_, ids := threeCloses(t, dbx)
	// Z-3 now looks older than Z-2.
	if _, err := dbx.d.DB.Exec(`UPDATE report_archive SET created_at = '2026-08-01 10:00:00' WHERE id = ?`, ids[2]); err != nil {
		t.Fatal(err)
	}
	ackAll(t, dbx)

	if n, err := dbx.repo.PruneReportArchiveAcked(ctx); err != nil || n != 1 {
		t.Fatalf("pruned n=%d err=%v, want 1 (only Z-1)", n, err)
	}
	left := archiveIDs(t, dbx)
	if len(left) != 2 || !left[ids[1]] || !left[ids[2]] {
		t.Fatalf("rows left = %v, want Z-2 (newest created_at) and Z-3 (highest z)", left)
	}
}

// Legacy rows carry no z_number: the newest-created_at rule alone protects.
func TestPruneReportArchive_NullZNumbersKeepNewest(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	_, ids := threeCloses(t, dbx)
	if _, err := dbx.d.DB.Exec(`UPDATE report_archive SET z_number = NULL, prev_z_number = NULL`); err != nil {
		t.Fatal(err)
	}
	ackAll(t, dbx)
	if n, err := dbx.repo.PruneReportArchiveAcked(ctx); err != nil || n != 2 {
		t.Fatalf("pruned n=%d err=%v, want 2", n, err)
	}
	if left := archiveIDs(t, dbx); len(left) != 1 || !left[ids[2]] {
		t.Fatalf("rows left = %v, want the newest", left)
	}
}

func TestPruneReportArchiveAcked_LeavesUnackedRows(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	_, ids := threeCloses(t, dbx)
	if _, err := dbx.repo.MarkReportArchiveAcked(ctx, ids[1], time.Now()); err != nil {
		t.Fatal(err)
	}
	if n, err := dbx.repo.PruneReportArchiveAcked(ctx); err != nil || n != 1 {
		t.Fatalf("pruned n=%d err=%v, want 1 (only the acked middle row)", n, err)
	}
	if left := archiveIDs(t, dbx); left[ids[1]] || !left[ids[0]] || !left[ids[2]] {
		t.Fatalf("rows left = %v, want the two un-acked rows", left)
	}
}

// Age-only (mode till, and every replica): a till whose every row is past
// the window still keeps its newest row.
func TestPruneReportArchiveOlderThan_KeepsNewestEvenWhenAllOld(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	_, ids := threeCloses(t, dbx)
	n, err := dbx.repo.PruneReportArchiveOlderThan(ctx, "2099-01-01")
	if err != nil || n != 2 {
		t.Fatalf("pruned n=%d err=%v, want 2", n, err)
	}
	if left := archiveIDs(t, dbx); len(left) != 1 || !left[ids[2]] {
		t.Fatalf("rows left = %v, want the newest", left)
	}
}

// Both: acked AND older than the cutoff.
func TestPruneReportArchiveAckedOlderThan(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	for _, p := range []string{"2010-01-01", "2011-01-01", "2012-01-01", "2026-01-01"} {
		if _, err := dbx.repo.ArchiveReport(ctx, "eod", p, []byte(`{}`), "", "", time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	// 2010 acked + old → deleted; 2011 old but un-acked → kept;
	// 2012 acked + old → deleted; 2026 acked but recent → kept.
	if _, err := dbx.d.DB.Exec(`UPDATE report_archive SET cloud_acked_at = '2026-10-01T00:00:00Z' WHERE period IN ('2010-01-01','2012-01-01','2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	n, err := dbx.repo.PruneReportArchiveAckedOlderThan(ctx, "2016-10-01")
	if err != nil || n != 2 {
		t.Fatalf("pruned n=%d err=%v, want 2", n, err)
	}
	for p, want := range map[string]bool{"2010-01-01": false, "2011-01-01": true, "2012-01-01": false, "2026-01-01": true} {
		if has, _ := dbx.repo.HasArchivedReport(ctx, "eod", p); has != want {
			t.Fatalf("period %s present=%v, want %v", p, has, want)
		}
	}
}

// The keep rule is per kind: another kind's newest row does not shield or
// expose an eod row.
func TestPruneReportArchive_KeepRuleIsPerKind(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	_, ids := threeCloses(t, dbx)
	if _, err := dbx.repo.ArchiveReport(ctx, "monthly", "2026-08", []byte(`{}`), "", "", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err := dbx.repo.ArchiveReport(ctx, "monthly", "2026-09", []byte(`{}`), "", "", time.Time{}); err != nil {
		t.Fatal(err)
	}
	ackAll(t, dbx)
	if n, err := dbx.repo.PruneReportArchiveAcked(ctx); err != nil || n != 3 {
		t.Fatalf("pruned n=%d err=%v, want 3 (2 eod + 1 monthly)", n, err)
	}
	left := archiveIDs(t, dbx)
	if len(left) != 2 || !left[ids[2]] {
		t.Fatalf("rows left = %v, want newest eod and newest monthly", left)
	}
	if has, _ := dbx.repo.HasArchivedReport(ctx, "monthly", "2026-09"); !has {
		t.Fatal("newest monthly row was pruned")
	}
}
