package data

import (
	"context"
	"testing"
	"time"
)

// sales.created_at exists in TWO shapes in a long-lived DB: RFC3339
// ("2026-08-10T04:00:00Z") written by the app's InsertSale path, and SQLite's
// own "2026-08-10 04:00:00" produced by the schema's DEFAULT (datetime('now'))
// for any row that never went through InsertSale. Every report-window
// predicate must treat the two identically at the boundary — which is what
// the datetime(created_at) wrapper on the column side buys (ut-docs#519).
// The existing window-bounds test only seeds the RFC3339 shape; this one
// covers both, mirrored across the same four boundary instants.
func TestMixedCreatedAtFormatsAtWindowBoundary(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	start := time.Date(2026, time.August, 10, 4, 0, 0, 0, time.UTC)
	end := time.Date(2026, time.August, 11, 4, 0, 0, 0, time.UTC)

	rfc := func(tm time.Time) string { return tm.UTC().Format(time.RFC3339) }
	sq := func(tm time.Time) string { return tm.UTC().Format("2006-01-02 15:04:05") }

	// SQLite-DEFAULT shape, same calendar day as start, one second BEFORE it.
	seedLifecycleSale(t, dbx, "sq-before", "A1", "sale", "completed", sq(start.Add(-time.Second)), 11, 0)
	// SQLite-DEFAULT shape, exactly AT start (inclusive).
	seedLifecycleSale(t, dbx, "sq-at-start", "A2", "sale", "completed", sq(start), 22, 0)
	// SQLite-DEFAULT shape, one second BEFORE end (inclusive).
	seedLifecycleSale(t, dbx, "sq-before-end", "A3", "sale", "completed", sq(end.Add(-time.Second)), 33, 0)
	// SQLite-DEFAULT shape, exactly AT end (exclusive).
	seedLifecycleSale(t, dbx, "sq-at-end", "A4", "sale", "completed", sq(end), 44, 0)
	// RFC3339 shape, mirrored at the same four instants.
	seedLifecycleSale(t, dbx, "rfc-before", "B1", "sale", "completed", rfc(start.Add(-time.Second)), 110, 0)
	seedLifecycleSale(t, dbx, "rfc-at-start", "B2", "sale", "completed", rfc(start), 220, 0)
	seedLifecycleSale(t, dbx, "rfc-before-end", "B3", "sale", "completed", rfc(end.Add(-time.Second)), 330, 0)
	seedLifecycleSale(t, dbx, "rfc-at-end", "B4", "sale", "completed", rfc(end), 440, 0)

	// Expected in-window: sq-at-start(22) + sq-before-end(33) + rfc-at-start(220)
	// + rfc-before-end(330) = 605, 4 sales.
	const wantTotal, wantCount = 605, 4

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
	if count != wantCount || total != wantTotal {
		t.Errorf("SalesByDay: got count=%d total=%d, want %d/%d (rows=%+v)", count, total, wantCount, wantTotal, daily)
	}

	cur, _, err := dbx.repo.PeriodComparison(ctx, start, end)
	if err != nil {
		t.Fatal(err)
	}
	if cur.Count != wantCount || cur.Total != wantTotal {
		t.Errorf("PeriodComparison current: got %+v, want %d/%d", cur, wantCount, wantTotal)
	}

	tills, err := dbx.repo.SalesByTill(ctx, start, end)
	if err != nil {
		t.Fatal(err)
	}
	var tillTotal int64
	var tillCount int
	for _, s := range tills {
		tillTotal += s.Revenue
		tillCount += s.Count
	}
	if tillCount != wantCount || tillTotal != wantTotal {
		t.Errorf("SalesByTill: got count=%d total=%d, want %d/%d", tillCount, tillTotal, wantCount, wantTotal)
	}

	buckets, err := dbx.repo.SalesByHour(ctx, start, end)
	if err != nil {
		t.Fatal(err)
	}
	var bTotal int64
	var bCount int
	for _, b := range buckets {
		bTotal += b.Total
		bCount += b.Count
	}
	if bCount != wantCount || bTotal != wantTotal {
		t.Errorf("SalesByHour: got count=%d total=%d, want %d/%d", bCount, bTotal, wantCount, wantTotal)
	}
}

// A same-second sale stored in the RFC3339 shape (the shape the real
// InsertSale path writes) must also survive the sub-second rolling end bound —
// TestRollingWindowIncludesSameSecondSale only covers the SQLite-DEFAULT shape.
func TestRollingSameSecondSaleRFC3339Shape(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)
	seedLifecycleSale(t, dbx, "fresh-rfc", "R1", "sale", "completed", now.Format(time.RFC3339), 100, 0)

	start := now.Add(-14 * 24 * time.Hour)
	end := now.Add(500 * time.Millisecond)
	daily, err := dbx.repo.SalesByDay(ctx, start, end)
	if err != nil {
		t.Fatal(err)
	}
	if len(daily) != 1 || daily[0].Count != 1 {
		t.Fatalf("same-second RFC3339 sale must be on the report, got %+v", daily)
	}
}
