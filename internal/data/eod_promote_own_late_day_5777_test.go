package data

import (
	"context"
	"testing"
	"time"
)

// ut-docs#5777 AC6 (Tester): marking an own sale late moves it into the
// promoted till's next Z only; its calendar-day placement (EndOfDayRange by
// local_date, SalesByDay by created_at) stays on the day it was made.
func TestMarkOwnLate_DayPlacementUnchanged_5777(t *testing.T) {
	d := b8OpenDB(t, "own-late-day.db")
	ctx := context.Background()
	repo, mirror := NewPOSRepo(d.DB), NewSalesMirrorRepo(d.DB)
	now := time.Now()
	t1 := now.Add(-72 * time.Hour) // made three days ago
	floor := now.Add(-2 * time.Hour)
	startCopy(t, mirror, "e1", 0)
	lateSale(t, d, "S", "", b8At(t1), b8At(t1), 700)
	s := testMirrorSale("S", "till-self", 100)
	s.LocalDate = now.Format("2006-01-02")
	at := b8At(now.Add(-time.Hour))
	s.CompletedAt = &at
	if _, err := mirror.ApplyFeedPage(ctx, MirrorFeedPage{Epoch: "e1", FromSeq: 0, ToSeq: 1,
		Records: []MirrorRecord{saleRec(1, s)}}, MirrorApplyOpts{SelfTillID: "till-self", Now: now}); err != nil {
		t.Fatal(err)
	}
	if n, err := mirror.MarkOwnLateArrivals(ctx, floor.UTC().Format(own5777Fmt), ""); err != nil || n != 1 {
		t.Fatalf("marked %d (%v), want 1", n, err)
	}
	var localDay string
	if err := d.QueryRow(`SELECT local_date FROM sales WHERE id = 'S'`).Scan(&localDay); err != nil {
		t.Fatal(err)
	}
	if rep, err := repo.EndOfDayRange(ctx, localDay, localDay); err != nil || rep.SalesCount != 1 || rep.Gross != 700 {
		t.Fatalf("EndOfDayRange(%s) = %d/%d (%v), want S on its own day", localDay, rep.SalesCount, rep.Gross, err)
	}
	today := now.Format("2006-01-02")
	if rep, err := repo.EndOfDayRange(ctx, today, today); err != nil || rep.SalesCount != 0 {
		t.Fatalf("EndOfDayRange(today) = %d (%v), want 0: S must not move to the recording day", rep.SalesCount, err)
	}
	days, err := repo.SalesByDay(ctx, now.Add(-5*24*time.Hour), now.Add(time.Hour), 0, 0)
	if err != nil || len(days) != 1 || days[0].Count != 1 || days[0].Day != t1.Format("2006-01-02") {
		t.Fatalf("SalesByDay = %+v (%v), want one day %s with S", days, err, t1.Format("2006-01-02"))
	}
}
