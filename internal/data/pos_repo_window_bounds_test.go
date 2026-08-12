package data

import (
	"context"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/reportperiod"
)

// lastDays is the rolling-window equivalent of the old day-count report
// signatures — [now - days*24h, now) — shared by the tests that kept their
// legacy "last N days" intent when the repo methods moved to explicit
// [start, end) windows (ut-docs#519).
func lastDays(days int) (time.Time, time.Time) {
	return reportperiod.RollingWindow(days, time.Now())
}

// The report queries take explicit [start, end) instants (ut-docs#519).
// created_at is stored RFC3339 ("...THH:MM:SSZ") by the app's InsertSale
// path, while the bind values are SQLite-canonical ("YYYY-MM-DD HH:MM:SS",
// UTC) — so the column side must normalize through datetime() or a same-day
// comparison degrades to a raw string compare where 'T' sorts after ' '
// (the PeriodComparison trap, confirmed live 2026-07-29). This test pins
// exactly that: boundaries with a NON-MIDNIGHT time-of-day, sales stored
// RFC3339 on the same calendar day either side of each bound.
func TestSalesByDayWindowBounds(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	start := time.Date(2026, time.August, 10, 4, 0, 0, 0, time.UTC)
	end := time.Date(2026, time.August, 11, 4, 0, 0, 0, time.UTC)

	rfc := func(tm time.Time) string { return tm.UTC().Format(time.RFC3339) }
	// Same calendar day as start, but BEFORE it — must be excluded. A raw
	// string compare against "2026-08-10 04:00:00" would wrongly include it
	// ('T' > ' ').
	seedLifecycleSale(t, dbx, "before-start", "R1", "sale", "completed", rfc(start.Add(-time.Hour)), 100, 0)
	// Exactly at start — inclusive.
	seedLifecycleSale(t, dbx, "at-start", "R2", "sale", "completed", rfc(start), 200, 0)
	// Mid-window.
	seedLifecycleSale(t, dbx, "mid", "R3", "sale", "completed", rfc(start.Add(10*time.Hour)), 300, 0)
	// Exactly at end — exclusive.
	seedLifecycleSale(t, dbx, "at-end", "R4", "sale", "completed", rfc(end), 400, 0)

	daily, err := dbx.repo.SalesByDay(ctx, start, end)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	var count int
	for _, d := range daily {
		total += d.Total
		count += d.Count
	}
	if count != 2 || total != 500 {
		t.Fatalf("expected [start, end) to keep exactly at-start+mid (2 sales, 500), got count=%d total=%d rows=%+v", count, total, daily)
	}
}

// RefundsByWindow gets the same explicit-window treatment.
func TestRefundsByWindowBounds(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	start := time.Date(2026, time.August, 10, 4, 0, 0, 0, time.UTC)
	end := time.Date(2026, time.August, 11, 4, 0, 0, 0, time.UTC)
	rfc := func(tm time.Time) string { return tm.UTC().Format(time.RFC3339) }

	seedLifecycleSale(t, dbx, "ref-before", "R1", "return", "completed", rfc(start.Add(-time.Minute)), 50, 0)
	seedLifecycleSale(t, dbx, "ref-in", "R2", "return", "completed", rfc(start.Add(time.Hour)), 70, 0)
	seedLifecycleSale(t, dbx, "ref-at-end", "R3", "return", "completed", rfc(end), 90, 0)

	total, count, err := dbx.repo.RefundsByWindow(ctx, start, end)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 || total != 70 {
		t.Fatalf("expected only the in-window refund (70), got total=%d count=%d", total, count)
	}
}

// A sale completed in the SAME second the report runs must already be on the
// report (the legacy day-count queries had no upper bound at all): a
// sub-second rolling-window end rounds UP to the next whole second rather
// than truncating, which would string-compare the fresh sale equal to the
// bound and silently drop it.
func TestRollingWindowIncludesSameSecondSale(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	// Stamp the sale in SQLite's own canonical "now" format (the DEFAULT
	// clause's shape) at this very second.
	now := time.Now().UTC().Truncate(time.Second)
	seedLifecycleSale(t, dbx, "fresh", "R1", "sale", "completed", now.Format("2006-01-02 15:04:05"), 100, 0)

	// end carries a nanosecond fraction inside the same second, like the
	// handler's time.Now() does.
	start, end := reportperiod.RollingWindow(14, now.Add(500*time.Millisecond))
	daily, err := dbx.repo.SalesByDay(ctx, start, end)
	if err != nil {
		t.Fatal(err)
	}
	if len(daily) != 1 || daily[0].Count != 1 || daily[0].Total != 100 {
		t.Fatalf("a same-second sale must be on the report, got %+v", daily)
	}
}

// PeriodComparison's year-ago window must shift by ONE CALENDAR YEAR
// (AddDate(-1,0,0)), not 365*24h: across a leap February a raw day-count
// drifts a day. Window [2028-03-01, 2028-03-08) shifted back a calendar year
// is [2027-03-01, 2027-03-08); a 365-day shift would give [2027-03-02, ...),
// wrongly dropping a sale on 2027-03-01.
func TestPeriodComparisonCalendarYearShift(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	start := time.Date(2028, time.March, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2028, time.March, 8, 0, 0, 0, 0, time.UTC)
	rfc := func(tm time.Time) string { return tm.UTC().Format(time.RFC3339) }

	seedLifecycleSale(t, dbx, "cur", "R1", "sale", "completed", rfc(start.Add(48*time.Hour)), 500, 0)
	// 2027-03-01 12:00 — first day of the calendar-shifted year-ago window,
	// OUTSIDE a raw 365-day-shifted one (2028 is a leap year; 2027-03-01 is
	// 366 days before 2028-03-01).
	seedLifecycleSale(t, dbx, "yr-edge", "R2", "sale", "completed", "2027-03-01T12:00:00Z", 300, 0)

	current, yearAgo, err := dbx.repo.PeriodComparison(ctx, start, end)
	if err != nil {
		t.Fatal(err)
	}
	if current.Count != 1 || current.Total != 500 {
		t.Fatalf("current = %+v, want 1 sale totalling 500", current)
	}
	if yearAgo.Count != 1 || yearAgo.Total != 300 {
		t.Fatalf("yearAgo = %+v, want the 2027-03-01 sale (calendar-year shift)", yearAgo)
	}
}
