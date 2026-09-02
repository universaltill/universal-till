package db

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// The report-query indexes below were introduced by migration 076
// (ut-docs#1319) and now live in the 001 baseline (ADR-0074); every test in
// this file asserts the final state of a fresh Open, so none needed the
// old ledger.
//
// TestReportQueryIndexesMakeDateRangePredicatesSargable runs the actual
// production query shapes from internal/data/pos_repo.go through SQLite's
// own planner (EXPLAIN QUERY PLAN) against a fresh migrated DB and confirms
// the expression indexes are what the planner picks — not a full scan, and
// not falling back to an existing equality-only index the way these
// queries did before those indexes existed (ut-docs#1319).
//
// Deliberately does NOT run ANALYZE: this product never calls ANALYZE
// anywhere (grep confirms), so a real till's sqlite_stat1 table never
// exists and the planner's no-stats default heuristics are what actually
// govern production — the case this test exercises. A single-column
// expression index on bare datetime(created_at) looked sufficient when
// checked WITH ANALYZE-informed stats during design, but without them the
// planner still preferred the plain status-equality index; only the
// composite (status, datetime(created_at)) shape actually wins under the
// real no-ANALYZE conditions, which is why this test asserts against real
// migrated schema rather than trusting the reasoning alone.
func TestReportQueryIndexesMakeDateRangePredicatesSargable(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "m076-plan.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	ctx := context.Background()

	// One row is enough — SQLite's no-stats planner heuristics are
	// structural (which index satisfies the most constraint terms), not
	// row-count-driven, so this doesn't need a synthetic 100k-row table to
	// be a faithful assertion of what the planner will pick in production.
	if _, err := d.DB.ExecContext(ctx, `INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, total, created_at)
		VALUES ('s1', 'R-1', 'completed', 'sale', 'GBP', 100, 100, '2024-06-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed sales: %v", err)
	}
	if _, err := d.DB.ExecContext(ctx, `INSERT INTO audit_log (id, entity_type, entity_id, action, created_at)
		VALUES ('a1', 'shift', 'sh1', 'cash_adjustment', '2024-06-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed audit_log: %v", err)
	}

	cases := []struct {
		name      string
		query     string
		args      []any
		wantIndex string
	}{
		{
			// Mirrors RefundsByWindow / SalesByDay / SalesByTill / etc.'s
			// shared predicate shape exactly (pos_repo.go).
			name:      "sales status+created_at range",
			query:     `SELECT COUNT(*) FROM sales WHERE status = 'completed' AND datetime(created_at) >= datetime(?) AND datetime(created_at) < datetime(?)`,
			args:      []any{"2024-06-01 00:00:00", "2024-06-02 00:00:00"},
			wantIndex: "idx_sales_status_created_dt",
		},
		{
			// Mirrors the shift cash-adjustment net query (pos_repo.go).
			name:      "audit_log entity_type+action+created_at range",
			query:     `SELECT COUNT(*) FROM audit_log WHERE entity_type = 'shift' AND action = 'cash_adjustment' AND datetime(created_at) >= datetime(?) AND datetime(created_at) < datetime(?)`,
			args:      []any{"2024-06-01 00:00:00", "2024-06-02 00:00:00"},
			wantIndex: "idx_audit_log_entity_action_created_dt",
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

// TestReportQueryIndexesDateRangeSummaryStaysUnfixed is a documentation-accuracy
// regression guard (review finding, ut-docs#1319): an earlier draft of
// migration 076's own comments wrongly claimed dateRangeSummary/
// EndOfDay(Range) were fixed by idx_sales_status_created_dt. They are not —
// pos_repo.go's dateRangeSummary uses date(created_at, 'localtime') BETWEEN,
// not datetime(created_at), the same non-indexable 'localtime' shape the
// migration deliberately left unfixed for worker_allocations/payments (see
// TestReportQueryIndexesDidNotIndexLocaltimeExpressions below for why). This
// test proves the composite index demotes to a status-only search for this
// exact query shape rather than silently becoming sargable (which would
// make that old claim right by accident) or silently regressing further.
// If this ever starts failing because the plan changed, this file's own
// "NOT fixed" notes need a matching update, not just this test.
func TestReportQueryIndexesDateRangeSummaryStaysUnfixed(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "m076-daterangesummary.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()

	if _, err := d.DB.ExecContext(ctx, `INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, total, created_at)
		VALUES ('s1', 'R-1', 'completed', 'sale', 'GBP', 100, 100, '2024-06-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed sales: %v", err)
	}

	// Mirrors dateRangeSummary's exact predicate (pos_repo.go).
	query := `SELECT COUNT(*) FROM sales WHERE status = 'completed' AND date(created_at, 'localtime') BETWEEN date(?) AND date(?)`
	rows, err := d.DB.QueryContext(ctx, "EXPLAIN QUERY PLAN "+query, "2024-06-01", "2024-06-02")
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
	// Expect exactly "idx_sales_status_created_dt (status=?)" — the date
	// bound reduced to a residual filter, NOT part of the index search
	// (which would show as "<expr>>? AND <expr><?" the way the datetime()
	// cases above do).
	if !strings.Contains(full, "idx_sales_status_created_dt (status=?)") {
		t.Fatalf("query plan = %q, expected dateRangeSummary's date(...,'localtime') predicate to stay unfixed (status-only search) — if this changed, update this file's NOT-fixed notes to match", full)
	}
	if strings.Contains(full, "<expr>") {
		t.Fatalf("query plan = %q, unexpectedly sargable on the date range — investigate before declaring this fixed", full)
	}
}

// TestReportQueryIndexesDidNotIndexLocaltimeExpressions is a regression guard for
// a real bug caught while building this migration: SQLite classifies
// date()/datetime()'s 'localtime' modifier as non-deterministic (it depends
// on the host's timezone database), and refuses to let a non-deterministic
// expression back a persisted index — CREATE INDEX itself succeeds (lazy
// validation), but the very next INSERT/UPDATE against the table then fails
// outright with "non-deterministic use of date() in an index". An earlier
// draft of migration 076 added exactly this (worker_allocations.allocated_at
// and payments.paid_at, matching worker_allocation_repo.go's
// date(col, 'localtime') query shape) and it would have broken every
// payment/allocation write in production. This test proves ordinary writes
// to both tables still succeed after 076 — if a future migration
// reintroduces a 'localtime'-expression index on either table, this fails
// immediately instead of silently shipping a checkout-breaking migration.
func TestReportQueryIndexesDidNotIndexLocaltimeExpressions(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "m076-localtime-writes.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()

	if _, err := d.DB.ExecContext(ctx, `INSERT INTO worker_allocations (id, source_type, cashier_id, amount_minor, allocated_at)
		VALUES ('wa1', 'yuzde_usulu_pool', 'c1', 50, '2024-06-01T00:00:00Z')`); err != nil {
		t.Fatalf("insert into worker_allocations must succeed: %v", err)
	}
	if _, err := d.DB.ExecContext(ctx, `INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, total, created_at)
		VALUES ('s1', 'R-1', 'completed', 'sale', 'GBP', 100, 100, '2024-06-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed sales: %v", err)
	}
	// 001_init.sql seeds default payment methods (id 'cash') via
	// INSERT OR IGNORE — no need to insert one here.
	if _, err := d.DB.ExecContext(ctx, `INSERT INTO payments (id, sale_id, method_id, amount, paid_at)
		VALUES ('p1', 's1', 'cash', 100, '2024-06-01T00:00:00Z')`); err != nil {
		t.Fatalf("insert into payments must succeed: %v", err)
	}
}

// TestReportQueryIndexesPlainIndexesExist confirms the three plain (non-expression)
// indexes this migration adds are actually used for the equality lookups
// they exist to fix (ut-docs#1319): variant_barcodes.variant_id and
// sale_links' two FK-style columns, all previously unindexed.
func TestReportQueryIndexesPlainIndexesExist(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "m076-plain.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()

	cases := []struct {
		name      string
		query     string
		wantIndex string
	}{
		{"variant_barcodes.variant_id", `SELECT barcode FROM variant_barcodes WHERE variant_id = 'v1'`, "idx_variant_barcodes_variant"},
		{"sale_links.sale_id", `SELECT original_sale_id FROM sale_links WHERE sale_id = 's1'`, "idx_sale_links_sale"},
		{"sale_links.original_sale_id", `SELECT sale_id FROM sale_links WHERE original_sale_id = 's1'`, "idx_sale_links_original_sale"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := d.DB.QueryContext(ctx, "EXPLAIN QUERY PLAN "+tc.query)
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
				t.Fatalf("query plan = %q, want it to use index %s", full, tc.wantIndex)
			}
		})
	}
}
