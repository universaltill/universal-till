package db

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// ctrlDay computes the shop-local calendar day for a literal timestamp via
// SQLite's own date(x, 'localtime') — the same control-query idiom
// b8ExpectedDay (internal/data/pos_repo_batch8_reports_test.go) and
// eod1012Day (internal/data/pos_repo_eod_1012_test.go) already use — rather
// than a hardcoded expected-date literal, so assertions hold on any host
// timezone, not just ones within a few hours of UTC (review finding,
// ut-docs#1342).
func ctrlDay(t *testing.T, d *DB, timestamp string) string {
	t.Helper()
	var day string
	if err := d.DB.QueryRow(`SELECT date(?, 'localtime')`, timestamp).Scan(&day); err != nil {
		t.Fatalf("control day query: %v", err)
	}
	return day
}

// Migration 007 (ut-docs#1342) adds sales.local_date/voided_local_date,
// worker_allocations.local_date and payments.local_date — precomputed
// local-calendar-day columns replacing the date(col, 'localtime') shape
// migration 076/ut-docs#1319 deliberately left unfixed on these same three
// tables (see report_query_indexes_test.go's
// TestReportQueryIndexesDidNotIndexLocaltimeExpressions), because SQLite
// refuses to let a non-deterministic expression back a persisted index. The
// columns are populated at write time (internal/data's InsertSale,
// InsertPayment, InsertWorkerAllocation, UpdateSaleStatus) rather than via a
// trigger: internal/db's own migration runner does not split CREATE
// TRIGGER ... BEGIN ... END blocks correctly (splitStatements' own doc
// comment: "No migration uses triggers or BEGIN…END blocks ... if one ever
// does, this splitter must learn them first") — verified empirically while
// drafting this migration, so the write-time-column approach was chosen
// instead of teaching the splitter a new construct for one card.

func TestReportQueryLocalDate_SalesAndWorkerAllocationRangesAreSargable(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "m007-plan.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()

	if _, err := d.DB.ExecContext(ctx, `INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, total, created_at, local_date)
		VALUES ('s1', 'R-1', 'completed', 'sale', 'GBP', 100, 100, '2024-06-01T00:00:00Z', '2024-06-01')`); err != nil {
		t.Fatalf("seed sales: %v", err)
	}
	if _, err := d.DB.ExecContext(ctx, `INSERT INTO worker_allocations (id, source_type, cashier_id, amount_minor, allocated_at, local_date)
		VALUES ('wa1', 'tip', 'c1', 50, '2024-06-01T00:00:00Z', '2024-06-01')`); err != nil {
		t.Fatalf("seed worker_allocations: %v", err)
	}
	if _, err := d.DB.ExecContext(ctx, `INSERT INTO payments (id, sale_id, method_id, amount, paid_at, local_date)
		VALUES ('p1', 's1', 'cash', 100, '2024-06-01T00:00:00Z', '2024-06-01')`); err != nil {
		t.Fatalf("seed payments: %v", err)
	}

	cases := []struct {
		name      string
		query     string
		args      []any
		wantIndex string
	}{
		{
			// dateRangeSummary's completed-sales totals query (pos_repo.go).
			name:      "sales status+local_date range",
			query:     `SELECT COUNT(*) FROM sales WHERE status = 'completed' AND local_date BETWEEN date(?) AND date(?)`,
			args:      []any{"2024-06-01", "2024-06-02"},
			wantIndex: "idx_sales_status_local_date",
		},
		{
			// dateRangeSummary's per-till breakdown query (pos_repo.go) — the
			// equality-shaped sibling of the range query above.
			name:      "sales status+local_date equality",
			query:     `SELECT COUNT(*) FROM sales WHERE status = 'completed' AND local_date = date(?)`,
			args:      []any{"2024-06-01"},
			wantIndex: "idx_sales_status_local_date",
		},
		{
			// WorkerAllocationsSummary/ListWorkerAllocations (worker_allocation_repo.go).
			name:      "worker_allocations source_type+local_date range",
			query:     `SELECT COUNT(*) FROM worker_allocations WHERE source_type = 'tip' AND local_date BETWEEN date(?) AND date(?)`,
			args:      []any{"2024-06-01", "2024-06-02"},
			wantIndex: "idx_worker_allocations_source_local_date",
		},
		{
			// WorkerAllocationsSummary's "tip" received-side query (joins payments).
			name:      "payments local_date range",
			query:     `SELECT COUNT(*) FROM payments WHERE local_date BETWEEN date(?) AND date(?)`,
			args:      []any{"2024-06-01", "2024-06-02"},
			wantIndex: "idx_payments_local_date",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := d.DB.QueryContext(ctx, "EXPLAIN QUERY PLAN "+tc.query, tc.args...)
			if err != nil {
				t.Fatalf("EXPLAIN QUERY PLAN: %v", err)
			}
			defer rows.Close()
			var plan []string
			for rows.Next() {
				var id, parent, notUsed int
				var detail string
				if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
					t.Fatalf("scan plan row: %v", err)
				}
				plan = append(plan, detail)
			}
			if err := rows.Err(); err != nil {
				t.Fatalf("plan rows: %v", err)
			}
			full := strings.Join(plan, " | ")
			if !strings.Contains(full, tc.wantIndex) {
				t.Fatalf("query plan = %q, want it to use index %s (sargable) not a full scan or a less-selective index", full, tc.wantIndex)
			}
		})
	}
}

// TestReportQueryLocalDate_VoidedCancellationSplitIsSargable proves
// dateRangeSummary's cancellations query — rewritten from a single
// COALESCE(voided_at, created_at) wrapped in date(...,'localtime') into an
// OR of two plain range comparisons against voided_local_date/local_date —
// actually uses an index on EACH branch rather than falling back to a
// status-only scan. A COALESCE across two columns cannot itself be
// satisfied by an index on either one, which is exactly why the query had
// to be split rather than simply pointed at a single new column.
func TestReportQueryLocalDate_VoidedCancellationSplitIsSargable(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "m007-voided-plan.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()

	if _, err := d.DB.ExecContext(ctx, `INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, total, created_at, local_date, voided_at, voided_local_date)
		VALUES ('s1', 'R-1', 'voided', 'sale', 'GBP', 100, 100, '2024-06-01T00:00:00Z', '2024-06-01', '2024-06-01T12:00:00Z', '2024-06-01')`); err != nil {
		t.Fatalf("seed sales: %v", err)
	}

	query := `
SELECT COUNT(*), COALESCE(SUM(total), 0)
FROM sales
WHERE status = 'voided' AND (
  (voided_local_date IS NOT NULL AND voided_local_date BETWEEN date(?) AND date(?))
  OR (voided_local_date IS NULL AND local_date BETWEEN date(?) AND date(?))
)`
	rows, err := d.DB.QueryContext(ctx, "EXPLAIN QUERY PLAN "+query, "2024-06-01", "2024-06-02", "2024-06-01", "2024-06-02")
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN: %v", err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatalf("scan plan row: %v", err)
		}
		plan = append(plan, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("plan rows: %v", err)
	}
	full := strings.Join(plan, " | ")
	if !strings.Contains(full, "idx_sales_status_voided_local_date") {
		t.Fatalf("query plan = %q, want the voided_local_date branch to use idx_sales_status_voided_local_date", full)
	}
	if !strings.Contains(full, "idx_sales_status_local_date") {
		t.Fatalf("query plan = %q, want the local_date fallback branch to use idx_sales_status_local_date", full)
	}
}

// TestReportQueryLocalDate_OrdinaryWritesStillSucceed mirrors
// TestReportQueryIndexesDidNotIndexLocaltimeExpressions above: local_date/
// voided_local_date are plain columns (never a 'localtime' expression
// itself backing an index), so ordinary inserts and the voided-status
// UPDATE must keep succeeding. Also proves UpdateSaleStatus's real
// production statement shape (CASE WHEN ... THEN date(?, 'localtime')
// ELSE ... END) — not just a literal test INSERT — writes cleanly.
func TestReportQueryLocalDate_OrdinaryWritesStillSucceed(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "m007-writes.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()

	if _, err := d.DB.ExecContext(ctx, `INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, total, created_at, local_date)
		VALUES ('s1', 'R-1', 'completed', 'sale', 'GBP', 100, 100, '2024-06-01T00:00:00Z', '2024-06-01')`); err != nil {
		t.Fatalf("insert into sales must succeed: %v", err)
	}
	if _, err := d.DB.ExecContext(ctx, `INSERT INTO worker_allocations (id, source_type, cashier_id, amount_minor, allocated_at, local_date)
		VALUES ('wa1', 'yuzde_usulu_pool', 'c1', 50, '2024-06-01T00:00:00Z', '2024-06-01')`); err != nil {
		t.Fatalf("insert into worker_allocations must succeed: %v", err)
	}
	if _, err := d.DB.ExecContext(ctx, `INSERT INTO payments (id, sale_id, method_id, amount, paid_at, local_date)
		VALUES ('p1', 's1', 'cash', 100, '2024-06-01T00:00:00Z', '2024-06-01')`); err != nil {
		t.Fatalf("insert into payments must succeed: %v", err)
	}

	// Mirrors UpdateSaleStatus's real statement shape.
	if _, err := d.DB.ExecContext(ctx, `
UPDATE sales
SET status = ?,
    voided_at = CASE WHEN ? = 'voided' THEN ? ELSE voided_at END,
    voided_local_date = CASE WHEN ? = 'voided' THEN date(?, 'localtime') ELSE voided_local_date END
WHERE id = ?
`, "voided", "voided", "2024-06-02T00:00:00Z", "voided", "2024-06-02T00:00:00Z", "s1"); err != nil {
		t.Fatalf("UpdateSaleStatus-shaped voided UPDATE must succeed: %v", err)
	}

	var voidedLocalDate string
	if err := d.DB.QueryRowContext(ctx, `SELECT voided_local_date FROM sales WHERE id = 's1'`).Scan(&voidedLocalDate); err != nil {
		t.Fatal(err)
	}
	want := ctrlDay(t, d, "2024-06-02T00:00:00Z")
	if voidedLocalDate != want {
		t.Fatalf("voided_local_date = %q, want %q", voidedLocalDate, want)
	}
}

// TestReportQueryLocalDate_BackfillRecomputesFromSourceColumn exercises
// migration 007's own backfill UPDATE statements (copied verbatim from
// internal/db/migrations/007_report_query_local_date_columns.sql) against
// rows whose local_date/voided_local_date are wrong or blank — the exact
// shape ALTER TABLE ... ADD COLUMN ... DEFAULT ” leaves behind on a real
// till's pre-existing rows before the backfill runs. Applied through the
// real per-statement migration runner (runMigrationSQL), not a raw Exec, so
// this also proves the backfill statements themselves are NOT mistaken for
// an ADD COLUMN by execMigrationStatements' regex.
func TestReportQueryLocalDate_BackfillRecomputesFromSourceColumn(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "m007-backfill.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()

	// A "pre-existing row" shape: local_date left at the column's own
	// DEFAULT '' (never populated), voided_at set but voided_local_date
	// still NULL — exactly what a till running an older build and then
	// upgrading would carry into this migration.
	if _, err := d.DB.ExecContext(ctx, `INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, total, created_at, voided_at)
		VALUES ('s1', 'R-1', 'voided', 'sale', 'GBP', 100, 100, '2024-06-01T09:00:00Z', '2024-06-02T10:00:00Z')`); err != nil {
		t.Fatalf("seed pre-existing sales row: %v", err)
	}
	if _, err := d.DB.ExecContext(ctx, `INSERT INTO worker_allocations (id, source_type, cashier_id, amount_minor, allocated_at)
		VALUES ('wa1', 'tip', 'c1', 50, '2024-06-01T09:00:00Z')`); err != nil {
		t.Fatalf("seed pre-existing worker_allocations row: %v", err)
	}
	if _, err := d.DB.ExecContext(ctx, `INSERT INTO payments (id, sale_id, method_id, amount, paid_at)
		VALUES ('p1', 's1', 'cash', 100, '2024-06-01T09:00:00Z')`); err != nil {
		t.Fatalf("seed pre-existing payments row: %v", err)
	}

	// Copied verbatim from migration 007's own backfill statements (live
	// tables only — this test doesn't seed the _archive twins). COALESCE
	// guards a malformed/unparseable source timestamp, whose date() would
	// otherwise return NULL against a NOT NULL column (review finding,
	// ut-docs#1342) — proven separately by
	// TestReportQueryLocalDate_BackfillToleratesMalformedTimestamp below.
	backfillSQL := `
UPDATE sales SET local_date = COALESCE(date(created_at, 'localtime'), '');
UPDATE sales SET voided_local_date = COALESCE(date(voided_at, 'localtime'), '') WHERE voided_at IS NOT NULL;
UPDATE worker_allocations SET local_date = COALESCE(date(allocated_at, 'localtime'), '');
UPDATE payments SET local_date = COALESCE(date(paid_at, 'localtime'), '');
`
	if err := runMigrationSQL(t, d, 9200, backfillSQL); err != nil {
		t.Fatalf("backfill migration: %v", err)
	}

	wantCreated := ctrlDay(t, d, "2024-06-01T09:00:00Z")
	wantVoided := ctrlDay(t, d, "2024-06-02T10:00:00Z")

	var salesLocalDate, voidedLocalDate string
	if err := d.DB.QueryRowContext(ctx, `SELECT local_date, voided_local_date FROM sales WHERE id = 's1'`).Scan(&salesLocalDate, &voidedLocalDate); err != nil {
		t.Fatal(err)
	}
	if salesLocalDate != wantCreated {
		t.Errorf("sales.local_date = %q, want %q (from created_at)", salesLocalDate, wantCreated)
	}
	if voidedLocalDate != wantVoided {
		t.Errorf("sales.voided_local_date = %q, want %q (from voided_at)", voidedLocalDate, wantVoided)
	}

	var waLocalDate string
	if err := d.DB.QueryRowContext(ctx, `SELECT local_date FROM worker_allocations WHERE id = 'wa1'`).Scan(&waLocalDate); err != nil {
		t.Fatal(err)
	}
	if waLocalDate != wantCreated {
		t.Errorf("worker_allocations.local_date = %q, want %q", waLocalDate, wantCreated)
	}

	var payLocalDate string
	if err := d.DB.QueryRowContext(ctx, `SELECT local_date FROM payments WHERE id = 'p1'`).Scan(&payLocalDate); err != nil {
		t.Fatal(err)
	}
	if payLocalDate != wantCreated {
		t.Errorf("payments.local_date = %q, want %q", payLocalDate, wantCreated)
	}
}

// TestReportQueryLocalDate_BackfillToleratesMalformedTimestamp proves the
// COALESCE(date(...), ”) guard in migration 007's backfill actually does
// its job: date() returns NULL for an unparseable timestamp, and local_date
// is NOT NULL, so an ungated backfill UPDATE would abort partway through
// with a constraint violation — which, since Open() runs migrations inline,
// would stop the till from starting on upgrade (review finding, ut-docs#1342:
// reproduced against the ungated form before this guard was added). Not
// hypothetical: internal/pages/sync_sales.go's own history (ut-docs#647)
// documents a malformed created_at arriving from a peer's journal before
// that fix landed, so such a row can exist on an unmigrated till today.
func TestReportQueryLocalDate_BackfillToleratesMalformedTimestamp(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "m007-backfill-malformed.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()

	if _, err := d.DB.ExecContext(ctx, `INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, total, created_at)
		VALUES ('s1', 'R-1', 'completed', 'sale', 'GBP', 100, 100, 'not-a-timestamp')`); err != nil {
		t.Fatalf("seed malformed-created_at row: %v", err)
	}

	backfillSQL := `UPDATE sales SET local_date = COALESCE(date(created_at, 'localtime'), '');`
	if err := runMigrationSQL(t, d, 9201, backfillSQL); err != nil {
		t.Fatalf("guarded backfill must tolerate a malformed timestamp, got: %v", err)
	}

	var localDate string
	if err := d.DB.QueryRowContext(ctx, `SELECT local_date FROM sales WHERE id = 's1'`).Scan(&localDate); err != nil {
		t.Fatal(err)
	}
	if localDate != "" {
		t.Errorf("local_date = %q, want '' (date() returns NULL for an unparseable timestamp, COALESCE falls back to empty)", localDate)
	}
}
