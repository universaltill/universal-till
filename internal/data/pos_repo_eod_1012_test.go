package data_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
)

// eod1012OpenDB mirrors department_report_test.go's own db.Open + seed-
// helper shape — a fresh, fully-migrated temp DB, not a hand-rolled schema
// (this package's own established convention for anything touching real
// FK'd tables like sales/sale_lines/categories).
func eod1012OpenDB(t *testing.T) (*db.DB, func(string, ...any)) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "eod1012.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	x := func(q string, args ...any) {
		if _, err := d.DB.Exec(q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	return d, x
}

// eod1012At renders a time in the RFC3339 format production writes to
// created_at/voided_at.
func eod1012At(tm time.Time) string { return tm.UTC().Format("2006-01-02T15:04:05Z") }

// eod1012Day computes the shop-local calendar day for tm via SQLite's own
// date(...,'localtime') — the same modifier dateRangeSummary/
// DepartmentsForDay use — rather than a hardcoded literal, so the
// assertion holds in every host timezone, not just UTC (ut-docs#559
// precedent: b8ExpectedDay in pos_repo_batch8_reports_test.go exists for
// exactly this reason, and this test deliberately places sales either
// side of a calendar-day boundary).
func eod1012Day(t *testing.T, d *db.DB, tm time.Time) string {
	t.Helper()
	var day string
	if err := d.DB.QueryRow(`SELECT date(?, 'localtime')`, eod1012At(tm)).Scan(&day); err != nil {
		t.Fatalf("control day query: %v", err)
	}
	return day
}

// ut-docs#1012 #1: a VOIDED sale (a completed receipt later cancelled/
// reversed — a "Storno") must be counted separately from a completed
// 'return' (a formal refund processed afterward — a "Retoure") — the
// reference day-close's own distinction between the two. Before this
// card, dateRangeSummary only ever scanned status = 'completed', so a
// voided sale was invisible to the day-close entirely: neither a sale, nor
// a refund, nor anything else. NOTE: this is a completed-then-voided
// sale, NOT a pre-tender abandoned basket — the 'sales' table has no row
// at all for a basket that never completed (see EODReport.CancelCount's
// own doc comment in pos_repo.go for why), so there is nothing earlier in
// the pipeline this counter could catch.
func TestEndOfDay_CancellationsCountedSeparatelyFromRefunds(t *testing.T) {
	d, x := eod1012OpenDB(t)

	dayStart := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	day1 := dayStart.Add(12 * time.Hour) // noon on day1 — safely mid-day
	day2 := dayStart.Add(36 * time.Hour) // noon on the day AFTER day1
	// Deliberately near the calendar-day boundary either side of day1, to
	// prove the query buckets by LOCAL day, not by a naive UTC-string
	// comparison — same shape as this package's own #869 boundary tests.
	beforeDay1 := dayStart.Add(-10 * time.Minute) // late on the PREVIOUS local day
	lateDay1 := dayStart.Add(20 * time.Hour)      // still within day1, far from midnight

	// A completed sale (counts toward Gross/SalesCount).
	x(`INSERT INTO sales (id, receipt_no, status, sale_type, subtotal, total, created_at)
	   VALUES ('s1','R1','completed','sale',1000,1000,?)`, eod1012At(day1))
	// A completed return / refund (counts toward RefundCount/RefundTotal).
	x(`INSERT INTO sales (id, receipt_no, status, sale_type, subtotal, total, created_at)
	   VALUES ('s2','R2','completed','return',300,300,?)`, eod1012At(day1))
	// A VOIDED sale — cancelled before tender. created_at is on the
	// PREVIOUS local day (parked overnight), voided_at is on day1: the
	// cancellation belongs to the day it was actually cancelled, not the
	// day the sale was opened.
	x(`INSERT INTO sales (id, receipt_no, status, sale_type, subtotal, total, created_at, voided_at)
	   VALUES ('s3','R3','voided','sale',750,750,?,?)`, eod1012At(beforeDay1), eod1012At(day1))
	// A second voided sale, same day, to prove it's a real SUM/COUNT, not
	// just "at least one".
	x(`INSERT INTO sales (id, receipt_no, status, sale_type, subtotal, total, created_at, voided_at)
	   VALUES ('s4','R4','voided','sale',250,250,?,?)`, eod1012At(lateDay1), eod1012At(lateDay1))
	// A voided sale on day2 must not leak into day1's count.
	x(`INSERT INTO sales (id, receipt_no, status, sale_type, subtotal, total, created_at, voided_at)
	   VALUES ('s5','R5','voided','sale',999,999,?,?)`, eod1012At(day2), eod1012At(day2))

	repo := data.NewPOSRepo(d.DB)
	rep, err := repo.EndOfDay(context.Background(), eod1012Day(t, d, day1))
	if err != nil {
		t.Fatalf("EndOfDay: %v", err)
	}

	if rep.CancelCount != 2 || rep.CancelTotal != 1000 {
		t.Fatalf("CancelCount/CancelTotal = %d/%d, want 2/1000 (s3 750 + s4 250, s5 excluded — different day)",
			rep.CancelCount, rep.CancelTotal)
	}
	if rep.RefundCount != 1 || rep.RefundTotal != 300 {
		t.Fatalf("RefundCount/RefundTotal = %d/%d, want 1/300 — a cancellation must never be counted as a refund",
			rep.RefundCount, rep.RefundTotal)
	}
	// A voided sale carries no revenue: Gross/Net must reflect ONLY the
	// completed sale, exactly as before this card.
	if rep.SalesCount != 1 || rep.Gross != 1000 {
		t.Fatalf("SalesCount/Gross = %d/%d, want 1/1000 — a voided sale must never inflate revenue",
			rep.SalesCount, rep.Gross)
	}
	if rep.Net != 700 { // 1000 gross - 300 refund; cancellations never participate
		t.Fatalf("Net = %d, want 700 — cancellations must not affect Net", rep.Net)
	}
}

// A day with zero cancellations must still report CancelCount=0,
// CancelTotal=0 (the zero value), never an error — same "always present,
// zero is a real answer" convention as RefundCount/RefundTotal.
func TestEndOfDay_NoCancellationsReportsZero(t *testing.T) {
	d, x := eod1012OpenDB(t)
	tm := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	x(`INSERT INTO sales (id, receipt_no, status, sale_type, subtotal, total, created_at)
	   VALUES ('s1','R1','completed','sale',1000,1000,?)`, eod1012At(tm))

	repo := data.NewPOSRepo(d.DB)
	rep, err := repo.EndOfDay(context.Background(), eod1012Day(t, d, tm))
	if err != nil {
		t.Fatalf("EndOfDay: %v", err)
	}
	if rep.CancelCount != 0 || rep.CancelTotal != 0 {
		t.Fatalf("CancelCount/CancelTotal = %d/%d, want 0/0 on a day with no voided sales",
			rep.CancelCount, rep.CancelTotal)
	}
}

// Independent review finding: a voided row with a NULL voided_at (never
// written by this codebase's own UpdateSaleStatus, which always stamps it
// — but not something the schema itself forbids) must not silently
// disappear from every window's count. COALESCE(voided_at, created_at)
// falls back to created_at so the row still lands on SOME day rather than
// becoming permanently uncountable.
func TestEndOfDay_VoidedSaleWithNullVoidedAtFallsBackToCreatedAt(t *testing.T) {
	d, x := eod1012OpenDB(t)
	tm := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	x(`INSERT INTO sales (id, receipt_no, status, sale_type, subtotal, total, created_at, voided_at)
	   VALUES ('s1','R1','voided','sale',400,400,?,NULL)`, eod1012At(tm))

	repo := data.NewPOSRepo(d.DB)
	rep, err := repo.EndOfDay(context.Background(), eod1012Day(t, d, tm))
	if err != nil {
		t.Fatalf("EndOfDay: %v", err)
	}
	if rep.CancelCount != 1 || rep.CancelTotal != 400 {
		t.Fatalf("CancelCount/CancelTotal = %d/%d, want 1/400 — a NULL voided_at must fall back to created_at, not vanish",
			rep.CancelCount, rep.CancelTotal)
	}
}

// ut-docs#1012 #3: a completed sale_line with quantity = 0 (a real unit
// price, "Storniert = nein" per the reference data — an item reduced to
// zero rather than removed) must not be dropped from the EOD Z-report's
// own department breakdown. There is no explicit "quantity > 0" filter
// anywhere in this query today, so this locks in that invariant with a
// regression test rather than fixing a bug — flag if this ever starts
// failing, which would mean a filter was added upstream without reading
// this card.
func TestDepartmentsForDay_QuantityZeroLineSurvives(t *testing.T) {
	d, x := eod1012OpenDB(t)
	tm := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)

	x(`INSERT INTO categories (id, name, parent_id) VALUES ('grocery','Grocery',NULL)`)
	x(`INSERT INTO items (id, name, base_price, category_id) VALUES ('milk','Milk',200,'grocery')`)

	x(`INSERT INTO sales (id, receipt_no, status, sale_type, subtotal, total, created_at)
	   VALUES ('s1','R1','completed','sale',200,200,?)`, eod1012At(tm))
	// Line 1: a normal sale of 1 unit.
	x(`INSERT INTO sale_lines (id, sale_id, line_no, item_id, name_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
	   VALUES ('l1','s1',1,'milk','Milk',1,200,0,0,200,200)`)
	// Line 2: quantity 0, a REAL unit price (500) and a non-zero
	// total_after_tax — an item reduced to zero rather than cancelled,
	// exactly the reference data's own shape ("8 lines with quantity
	// 0.00, a real unit price, and Storniert = nein"). It must still
	// contribute its revenue to the department breakdown.
	x(`INSERT INTO sale_lines (id, sale_id, line_no, item_id, name_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
	   VALUES ('l2','s1',2,'milk','Milk',0,500,0,0,500,500)`)

	repo := data.NewPOSRepo(d.DB)
	rows, err := repo.DepartmentsForDay(context.Background(), eod1012Day(t, d, tm))
	if err != nil {
		t.Fatalf("DepartmentsForDay: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected exactly 1 department row, got %+v", rows)
	}
	g := rows[0]
	if g.Department != "Grocery" {
		t.Fatalf("department = %q, want Grocery", g.Department)
	}
	// Qty: 1 (line 1) + 0 (line 2) = 1 -- the zero-qty line contributes to
	// the SUM without being excluded by any filter.
	if g.Qty != 1 {
		t.Fatalf("Qty = %v, want 1 (quantity-zero line must not be silently dropped from the SUM)", g.Qty)
	}
	// Revenue: 200 (line 1) + 500 (line 2) = 700 -- if the zero-qty line
	// were dropped, this would read 200, silently losing real revenue.
	if g.Revenue != 700 {
		t.Fatalf("Revenue = %d, want 700 — the quantity-zero line's revenue must survive", g.Revenue)
	}
}

// Same invariant as TestDepartmentsForDay_QuantityZeroLineSurvives, but
// against SalesForTaxBands — the query directly behind the Z-report's own
// "BY VAT RATE" section (independent review finding: this is the one
// query in the file with a NEAR-miss line filter,
// `sl.total_before_tax != 0 OR sl.total_after_tax != 0`, so it's the query
// where a future filter tightening would actually land). A quantity-zero
// line with a real (non-zero) unit price and total — the reference data's
// own shape — has non-zero totals, so it passes this filter today; this
// pins that down explicitly rather than relying on DepartmentsForDay
// alone to stand in for every line-scanning query in this file.
func TestSalesForTaxBands_QuantityZeroLineWithRealTotalSurvives(t *testing.T) {
	d, x := eod1012OpenDB(t)
	tm := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)

	x(`INSERT INTO items (id, name, base_price) VALUES ('milk','Milk',200)`)
	x(`INSERT INTO sales (id, receipt_no, status, sale_type, subtotal, total, created_at)
	   VALUES ('s1','R1','completed','sale',200,200,?)`, eod1012At(tm))
	x(`INSERT INTO sale_lines (id, sale_id, line_no, item_id, name_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
	   VALUES ('l1','s1',1,'milk','Milk',1,200,0,0,200,200)`)
	x(`INSERT INTO sale_lines (id, sale_id, line_no, item_id, name_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
	   VALUES ('l2','s1',2,'milk','Milk',0,500,0,0,500,500)`)

	repo := data.NewPOSRepo(d.DB)
	day := eod1012Day(t, d, tm)
	sales, err := repo.SalesForTaxBands(context.Background(), day, day)
	if err != nil {
		t.Fatalf("SalesForTaxBands: %v", err)
	}
	if len(sales) != 1 {
		t.Fatalf("expected exactly 1 sale, got %+v", sales)
	}
	if len(sales[0].Lines) != 2 {
		t.Fatalf("expected both lines (including the quantity-zero one) to survive, got %+v", sales[0].Lines)
	}
	var total int64
	for _, l := range sales[0].Lines {
		total += l.LineTotal
	}
	if total != 700 {
		t.Fatalf("summed LineTotal = %d, want 700 — the quantity-zero line's total must survive", total)
	}
}
