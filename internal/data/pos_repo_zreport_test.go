package data

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"
)

// Z-number chaining on report_archive (ut-docs#1080). These tests assert the
// BEHAVIOR of the gapless sequence -- first close numbered 1 with no
// predecessor, each later close chained to the previous, duplicates consuming
// no number, and true concurrent closes never duplicating or gapping -- not
// the SQL that implements it.

// findArchived returns the row for (kind, period) from ListArchivedReports.
func findArchived(t *testing.T, repo *POSRepo, kind, period string) ArchivedReportRow {
	t.Helper()
	rows, err := repo.ListArchivedReports(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Kind == kind && r.Period == period {
			return r
		}
	}
	t.Fatalf("no archived row for kind=%q period=%q in %+v", kind, period, rows)
	return ArchivedReportRow{}
}

func TestArchiveReport_FirstCloseGetsZNumberOneNoPredecessor(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	created, err := dbx.repo.ArchiveReport(ctx, "eod", "2026-01-01", []byte(`{}`), "R001", "R009", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("expected first archive to report created=true")
	}

	// MAX() over zero matching rows still yields exactly one row (NULL), so
	// COALESCE(...,0)+1 must produce 1 and the predecessor fields NULL.
	row := findArchived(t, dbx.repo, "eod", "2026-01-01")
	if row.ZNumber != 1 {
		t.Fatalf("expected z_number=1 for the first-ever close, got %d", row.ZNumber)
	}
	if row.PrevZNumber != nil {
		t.Fatalf("expected prev_z_number nil for the first-ever close, got %d", *row.PrevZNumber)
	}
	if row.PrevClosedAt != nil {
		t.Fatalf("expected prev_closed_at nil for the first-ever close, got %q", *row.PrevClosedAt)
	}
}

func TestArchiveReport_SecondCloseChainsToFirst(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	if _, err := dbx.repo.ArchiveReport(ctx, "eod", "2026-01-01", []byte(`{}`), "", "", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err := dbx.repo.ArchiveReport(ctx, "eod", "2026-01-02", []byte(`{}`), "", "", time.Time{}); err != nil {
		t.Fatal(err)
	}

	first := findArchived(t, dbx.repo, "eod", "2026-01-01")
	second := findArchived(t, dbx.repo, "eod", "2026-01-02")
	if second.ZNumber != 2 {
		t.Fatalf("expected z_number=2 for the second close, got %d", second.ZNumber)
	}
	if second.PrevZNumber == nil || *second.PrevZNumber != 1 {
		t.Fatalf("expected prev_z_number=1, got %v", second.PrevZNumber)
	}
	if second.PrevClosedAt == nil || *second.PrevClosedAt != first.CreatedAt {
		t.Fatalf("expected prev_closed_at=%q (first close's created_at), got %v",
			first.CreatedAt, second.PrevClosedAt)
	}
}

func TestArchiveReport_DuplicatePeriodConsumesNoNumber(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	if _, err := dbx.repo.ArchiveReport(ctx, "eod", "2026-01-01", []byte(`{}`), "", "", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err := dbx.repo.ArchiveReport(ctx, "eod", "2026-01-02", []byte(`{}`), "", "", time.Time{}); err != nil {
		t.Fatal(err)
	}

	// Re-archiving an existing (kind, period) is a no-op...
	created, err := dbx.repo.ArchiveReport(ctx, "eod", "2026-01-02", []byte(`{"clobber":true}`), "", "", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("expected the duplicate archive to report created=false")
	}

	// ...and it must not have consumed a number: the next genuinely new
	// period continues the sequence right after the last real row.
	if _, err := dbx.repo.ArchiveReport(ctx, "eod", "2026-01-03", []byte(`{}`), "", "", time.Time{}); err != nil {
		t.Fatal(err)
	}
	third := findArchived(t, dbx.repo, "eod", "2026-01-03")
	if third.ZNumber != 3 {
		t.Fatalf("expected z_number=3 (duplicate call consumed no number), got %d", third.ZNumber)
	}
}

func TestArchiveReport_ConcurrentClosesAreGaplessAndUnique(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)

	// N goroutines, each a distinct period, all against the SAME *sql.DB
	// (pooled connections, so the statements genuinely contend). What this
	// pins is the end-to-end invariant: concurrent closes never duplicate,
	// skip or reorder a number. It does NOT exercise ArchiveReport's retry
	// loop -- ut-docs#1080's review measured zero lost races on this DSN
	// (an autocommit INSERT...SELECT holds SQLite's write lock while it
	// evaluates MAX()), so the retry is defence in depth. See
	// ArchiveReport's doc comment.
	const n = 10
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			period := fmt.Sprintf("2026-02-%02d", i+1)
			_, errs[i] = dbx.repo.ArchiveReport(context.Background(), "eod", period, []byte(`{}`), "", "", time.Time{})
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent archive %d failed: %v", i, err)
		}
	}

	rows, err := dbx.repo.ListArchivedReports(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != n {
		t.Fatalf("expected %d archived rows, got %d", n, len(rows))
	}
	got := make([]int64, 0, n)
	for _, r := range rows {
		got = append(got, r.ZNumber)
	}
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	for i, z := range got {
		if z != int64(i+1) {
			t.Fatalf("expected z_numbers exactly 1..%d with no gaps or duplicates, got %v", n, got)
		}
	}
}

func TestArchiveReport_ReceiptRangeRoundTrips(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	if _, err := dbx.repo.ArchiveReport(ctx, "eod", "2026-01-01", []byte(`{}`), "R010", "R042", time.Time{}); err != nil {
		t.Fatal(err)
	}

	listed := findArchived(t, dbx.repo, "eod", "2026-01-01")
	if listed.FirstReceipt != "R010" || listed.LastReceipt != "R042" {
		t.Fatalf("ListArchivedReports: expected receipt range R010..R042, got %q..%q",
			listed.FirstReceipt, listed.LastReceipt)
	}

	ranged, err := dbx.repo.ArchivedReportsInRange(ctx, "2026-01-01", "2026-01-01")
	if err != nil {
		t.Fatal(err)
	}
	if len(ranged) != 1 {
		t.Fatalf("expected 1 row in range, got %d", len(ranged))
	}
	if ranged[0].FirstReceipt != "R010" || ranged[0].LastReceipt != "R042" {
		t.Fatalf("ArchivedReportsInRange: expected receipt range R010..R042, got %q..%q",
			ranged[0].FirstReceipt, ranged[0].LastReceipt)
	}
	if ranged[0].ZNumber != 1 || ranged[0].PrevZNumber != nil || ranged[0].PrevClosedAt != nil {
		t.Fatalf("ArchivedReportsInRange: expected z_number=1 with nil predecessor, got %+v", ranged[0])
	}
}

func TestArchiveReport_LegacyNullRowCoexistsWithNumberedRows(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	// Simulate a pre-migration row: inserted without any z-number columns,
	// so they stay NULL. The partial unique index must not reject it.
	if _, err := dbx.d.DB.ExecContext(ctx, `
INSERT INTO report_archive (id, kind, period, content_json)
VALUES ('legacy1', 'eod', '2025-12-30', '{}'), ('legacy2', 'eod', '2025-12-31', '{}')`); err != nil {
		t.Fatalf("two legacy NULL-z_number rows must coexist under the partial unique index: %v", err)
	}

	// A new numbered close starts the sequence at 1 and does NOT treat the
	// legacy (unnumbered) rows as a predecessor -- prev_closed_at must stay
	// nil even though older kind='eod' rows exist.
	if _, err := dbx.repo.ArchiveReport(ctx, "eod", "2026-01-01", []byte(`{}`), "", "", time.Time{}); err != nil {
		t.Fatal(err)
	}
	numbered := findArchived(t, dbx.repo, "eod", "2026-01-01")
	if numbered.ZNumber != 1 || numbered.PrevZNumber != nil || numbered.PrevClosedAt != nil {
		t.Fatalf("expected first numbered close after legacy rows to be z=1 with nil predecessor, got %+v", numbered)
	}

	// Legacy rows read back with ZNumber==0 ("no number" -- real numbers
	// start at 1, so 0 is unambiguous) and nil predecessor fields.
	legacy := findArchived(t, dbx.repo, "eod", "2025-12-30")
	if legacy.ZNumber != 0 || legacy.PrevZNumber != nil || legacy.PrevClosedAt != nil {
		t.Fatalf("expected legacy row to read back as z=0 with nil predecessor, got %+v", legacy)
	}
	if legacy.FirstReceipt != "" || legacy.LastReceipt != "" {
		t.Fatalf("expected legacy row's NULL receipt range to read back empty, got %q..%q",
			legacy.FirstReceipt, legacy.LastReceipt)
	}
}

// The gapless guarantee rests on a DB-level constraint, not on Go: if
// ux_report_archive_kind_znumber were ever downgraded to a plain (non-
// UNIQUE) index, every test above would still pass -- they only ever go
// through ArchiveReport, which allocates correctly on its own -- while the
// database quietly stopped rejecting a duplicate Z-number arriving any
// other way (a restore, a repair script, a future writer). This pins the
// constraint itself by writing straight past the repository.
func TestArchiveReport_ZNumberUniquenessIsEnforcedByTheDatabase(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	if _, err := dbx.repo.ArchiveReport(ctx, "eod", "2026-01-01", []byte(`{}`), "", "", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err := dbx.d.DB.ExecContext(ctx, `
INSERT INTO report_archive (id, kind, period, content_json, z_number)
VALUES ('dupz', 'eod', '2026-01-02', '{}', 1)`); err == nil {
		t.Fatal("expected the database to reject a second (kind, z_number) = (eod, 1) row")
	}

	// Same z_number under a different kind is fine -- the index is
	// kind-scoped, so each kind carries its own sequence.
	if _, err := dbx.d.DB.ExecContext(ctx, `
INSERT INTO report_archive (id, kind, period, content_json, z_number)
VALUES ('otherkind', 'shift', '2026-01-02', '{}', 1)`); err != nil {
		t.Fatalf("(shift, 1) alongside (eod, 1) must be allowed: %v", err)
	}
}

// ArchiveReport scopes MAX(z_number) and the prev_closed_at lookup by kind,
// and migration 070's index is (kind, z_number) -- so a second report kind
// ("weekly/monthly later", per 013's own comment) gets its own sequence
// starting at 1 rather than continuing the EOD one or chaining across.
func TestArchiveReport_SequenceIsScopedPerKind(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	for _, p := range []string{"2026-01-01", "2026-01-02"} {
		if _, err := dbx.repo.ArchiveReport(ctx, "eod", p, []byte(`{}`), "", "", time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := dbx.repo.ArchiveReport(ctx, "weekly", "2026-W01", []byte(`{}`), "", "", time.Time{}); err != nil {
		t.Fatal(err)
	}

	if got := findArchived(t, dbx.repo, "eod", "2026-01-02").ZNumber; got != 2 {
		t.Fatalf("expected the second eod close to be z_number=2, got %d", got)
	}
	weekly := findArchived(t, dbx.repo, "weekly", "2026-W01")
	if weekly.ZNumber != 1 {
		t.Fatalf("expected the first weekly close to start its own sequence at 1, got %d", weekly.ZNumber)
	}
	if weekly.PrevZNumber != nil || weekly.PrevClosedAt != nil {
		t.Fatalf("expected no predecessor for a kind's first close, got prev_z=%v prev_closed=%v",
			weekly.PrevZNumber, weekly.PrevClosedAt)
	}
}
