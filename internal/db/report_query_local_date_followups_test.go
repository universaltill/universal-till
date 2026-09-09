package db

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// ut-docs#1664 (follow-up to ut-docs#1342/migration 007): extends the
// local_date sargability fix to the read sites migration 007's own review
// deliberately left unfixed — SalesForTaxBands, the day-only report
// functions (DepartmentsForDay/ArticleGroupsForDay/ArticleSalesForDay/
// OperatorSalesForDay/OrderTypeSalesForDay), DayTotal and ListSalesJournal's
// Day filter. All of these compare a plain shop-local CALENDAR-MIDNIGHT day
// (local_date), unlike busyBuckets/SalesByDay/SalesByWeekday/SalesByHour,
// which apply the ADR-0057 business-day-start hh:mm shift directly to
// created_at inside their GROUP BY bucket expression — a materially
// different, mutable-setting-dependent semantic deliberately left
// unconverted here (see this card's own scope note).
//
// Migration 021 adds idx_sales_local_date (local_date alone, no status
// prefix) for ListSalesJournal specifically: it is the one query in this
// group with no status predicate at all (the journal intentionally shows
// every status), so idx_sales_status_local_date's (status, local_date)
// shape can't back its Day filter. Every other site here filters on
// status = 'completed' first and reuses migration 007's existing
// idx_sales_status_local_date — no new index needed for those.

func TestReportQueryLocalDateFollowups_DayOnlyReportFunctionsAreSargable(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "m1664-day-only-plan.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()

	if _, err := d.DB.ExecContext(ctx, `INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, total, created_at, local_date)
		VALUES ('s1', 'R-1', 'completed', 'sale', 'GBP', 100, 100, '2024-06-01T00:00:00Z', '2024-06-01')`); err != nil {
		t.Fatalf("seed sales: %v", err)
	}

	// DepartmentsForDay / ArticleGroupsForDay / ArticleSalesForDay /
	// OperatorSalesForDay / OrderTypeSalesForDay all share this exact
	// WHERE shape (pos_repo.go): status = 'completed' AND sale_type = 'sale'
	// AND s.local_date = date(?).
	query := `SELECT COUNT(*) FROM sales s WHERE s.status = 'completed' AND s.sale_type = 'sale' AND s.local_date = date(?)`
	rows, err := d.DB.QueryContext(ctx, "EXPLAIN QUERY PLAN "+query, "2024-06-01")
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
	if !strings.Contains(full, "idx_sales_status_local_date") {
		t.Fatalf("query plan = %q, want it to use idx_sales_status_local_date (sargable), not a full scan", full)
	}
}

func TestReportQueryLocalDateFollowups_SalesForTaxBandsIsSargable(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "m1664-taxbands-plan.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()

	if _, err := d.DB.ExecContext(ctx, `INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, total, created_at, local_date)
		VALUES ('s1', 'R-1', 'completed', 'sale', 'GBP', 100, 100, '2024-06-01T00:00:00Z', '2024-06-01')`); err != nil {
		t.Fatalf("seed sales: %v", err)
	}

	cases := []struct {
		name  string
		query string
	}{
		{
			// SalesForTaxBands' own sales-header query (pos_repo.go).
			name:  "sales header",
			query: `SELECT COUNT(*) FROM sales WHERE status = 'completed' AND local_date BETWEEN date(?) AND date(?)`,
		},
		{
			// SalesForTaxBands' sale_lines-join query (aliased s.local_date).
			name:  "sale_lines join",
			query: `SELECT COUNT(*) FROM sale_lines sl JOIN sales s ON s.id = sl.sale_id WHERE s.status = 'completed' AND s.local_date BETWEEN date(?) AND date(?)`,
		},
		{
			// SalesForTaxBands' payments-join query (aliased s.local_date).
			name:  "payments join",
			query: `SELECT COUNT(*) FROM payments p JOIN sales s ON s.id = p.sale_id WHERE s.status = 'completed' AND s.local_date BETWEEN date(?) AND date(?)`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := d.DB.QueryContext(ctx, "EXPLAIN QUERY PLAN "+tc.query, "2024-06-01", "2024-06-02")
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
			if !strings.Contains(full, "idx_sales_status_local_date") {
				t.Fatalf("query plan = %q, want the sales side to use idx_sales_status_local_date (sargable)", full)
			}
		})
	}
}

func TestReportQueryLocalDateFollowups_DayTotalIsSargable(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "m1664-daytotal-plan.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()

	if _, err := d.DB.ExecContext(ctx, `INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, total, created_at, local_date)
		VALUES ('s1', 'R-1', 'completed', 'sale', 'GBP', 100, 100, '2024-06-01T00:00:00Z', '2024-06-01')`); err != nil {
		t.Fatalf("seed sales: %v", err)
	}

	// DayTotal's own shape (pos_repo.go): the daysAgo shift applies to ref
	// (the right-hand literal), not to the stored column — plain
	// calendar-midnight arithmetic, not the ADR-0057 business-day-start
	// shift, so local_date is the correct, sargable fit.
	query := `SELECT COALESCE(SUM(total), 0), COUNT(*) FROM sales
WHERE status = 'completed' AND sale_type = 'sale'
  AND local_date = date(?, 'localtime', ?)`
	rows, err := d.DB.QueryContext(ctx, "EXPLAIN QUERY PLAN "+query, "2024-06-02T00:00:00Z", "-1 days")
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
	if !strings.Contains(full, "idx_sales_status_local_date") {
		t.Fatalf("query plan = %q, want it to use idx_sales_status_local_date (sargable)", full)
	}
}

// TestReportQueryLocalDateFollowups_ListSalesJournalDayFilterIsSargable
// covers the one query in this group with NO status predicate at all (the
// sales journal intentionally shows every status) — idx_sales_status_local_date
// can't back it, which is exactly why migration 021 adds a plain
// idx_sales_local_date (local_date alone).
func TestReportQueryLocalDateFollowups_ListSalesJournalDayFilterIsSargable(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "m1664-journal-plan.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()

	if _, err := d.DB.ExecContext(ctx, `INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, total, created_at, local_date)
		VALUES ('s1', 'R-1', 'completed', 'sale', 'GBP', 100, 100, '2024-06-01T00:00:00Z', '2024-06-01')`); err != nil {
		t.Fatalf("seed sales: %v", err)
	}

	// ListSalesJournal's Day-filter branch, AllTills=true (no till_id
	// predicate) — the WHERE clause it actually builds.
	query := `SELECT s.receipt_no FROM sales s WHERE 1=1 AND s.local_date = date(?) ORDER BY s.created_at DESC LIMIT ?`
	rows, err := d.DB.QueryContext(ctx, "EXPLAIN QUERY PLAN "+query, "2024-06-01", 6)
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
	if !strings.Contains(full, "idx_sales_local_date") {
		t.Fatalf("query plan = %q, want it to use idx_sales_local_date (sargable, no status prefix needed)", full)
	}
}
