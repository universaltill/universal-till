package pages

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#5831 (ADR-0066 amendment): the Z close bound is the next whole
// second after the clock, so a row stamped in the same second as the close
// belongs to THIS Z, and the lock is held until the clock reaches the bound.

// w5831Clock swaps eodClock for a fixed instant and a recording waiter, and
// returns the call log. Restored on cleanup.
type w5831Clock struct {
	now     time.Time
	waited  []time.Time
	waitErr error
	// heldAtWait records, per wait, whether the hold ctx was still live:
	// releaseHeld cancels it, so false means the wait ran after release.
	heldAtWait []bool
}

func setW5831Clock(t *testing.T, now time.Time) *w5831Clock {
	t.Helper()
	c := &w5831Clock{now: now}
	old := eodClock
	eodClock.now = func() time.Time { return c.now }
	eodClock.waitUntil = func(ctx context.Context, until time.Time) error {
		c.waited = append(c.waited, until)
		c.heldAtWait = append(c.heldAtWait, ctx.Err() == nil)
		return c.waitErr
	}
	t.Cleanup(func() { eodClock = old })
	return c
}

// w5831Base is a fixed-offset instant far outside the fixed-date window, at
// a mid-day whole second plus a sub-second part (derived from the real clock
// per guard-fixed-dates.sh).
func w5831Base() time.Time {
	d := time.Now().UTC().AddDate(0, 0, -200)
	return time.Date(d.Year(), d.Month(), d.Day(), 12, 0, 30, 400_000_000, time.UTC)
}

func w5831Shift(t *testing.T, dp *common.Deps, id string, closedAt time.Time, counted, expected int64) {
	t.Helper()
	if _, err := dp.Db.Exec(`INSERT OR IGNORE INTO registers(id,name,is_active) VALUES('reg1','Front Till',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO shifts(id,register_id,cashier_id,opened_at,closed_at,opening_cash,closing_cash,expected_cash,new_float)
VALUES(?,?,?,?,?,?,?,?,?)`, id, "reg1", "user1", closedAt.Add(-time.Hour).UTC().Format(time.RFC3339),
		closedAt.UTC().Format(time.RFC3339), 1000, counted, expected, counted); err != nil {
		t.Fatalf("seed shift %s: %v", id, err)
	}
}

func w5831CashSale(t *testing.T, dp *common.Deps, id, receipt string, total int64, at time.Time) {
	t.Helper()
	ectcSeedSale(t, dp, id, receipt, total, at)
	if _, err := dp.Db.Exec(`INSERT INTO payments (id, sale_id, method_id, amount, currency, change_given, tip_amount, paid_at)
VALUES (?, ?, 'cash', ?, 'GBP', 0, 0, ?)`, "pay-"+id, id, total, at.UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("seed payment %s: %v", id, err)
	}
}

func TestZCloseBound_AlwaysNextWholeSecond(t *testing.T) {
	s := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC) // fixed-date:ok pure function, no clock
	for _, ns := range []int{0, 1, 999_999_999} {
		got := zCloseBound(s.Add(time.Duration(ns)))
		if want := s.Add(time.Second); !got.Equal(want) {
			t.Fatalf("zCloseBound(S+%dns) = %v, want %v", ns, got, want)
		}
	}
}

func TestGenerateEOD_SameSecondShiftCloseInThisZ(t *testing.T) {
	dp := newEODTestDeps(t)
	base := w5831Base()
	setW5831Clock(t, base)
	w5831Shift(t, dp, "sh5831", base.Truncate(time.Second), 1500, 1400)

	rep1, created, err := generateEOD(t.Context(), dp, "system", "", "")
	if err != nil || !created {
		t.Fatalf("first Z: created=%v err=%v", created, err)
	}
	if rep1.CashReconciliation == nil || rep1.CashReconciliation.ShiftsClosed != 1 {
		t.Fatalf("same-second shift must be in this Z, got %+v", rep1.CashReconciliation)
	}
	if rc := rep1.CashReconciliation; rc.Counted != 1500 || rc.Calculated != 1400 || rc.Variance != 100 {
		t.Fatalf("reconciliation mismatch: %+v", rc)
	}

	repo := data.NewPOSRepo(dp.Db)
	from := *mustLatest(t, repo)
	rec2, err := repo.CashReconciliationForInstantWindow(t.Context(), from, from.Add(24*time.Hour))
	if err != nil || rec2 != nil {
		t.Fatalf("shift must not be in the next window: rec=%+v err=%v", rec2, err)
	}
}

func mustLatest(t *testing.T, repo *data.POSRepo) *time.Time {
	t.Helper()
	l, err := repo.LatestArchivedAt(t.Context(), "eod")
	if err != nil || l == nil {
		t.Fatalf("LatestArchivedAt: %v err=%v", l, err)
	}
	return l
}

func TestGenerateEOD_SameSecondSaleInThisZ(t *testing.T) {
	dp := newEODTestDeps(t)
	base := w5831Base()
	setW5831Clock(t, base)
	w5831CashSale(t, dp, "s5831", "R-5831", 700, base.Truncate(time.Second))

	rep, created, err := generateEOD(t.Context(), dp, "system", "", "")
	if err != nil || !created {
		t.Fatalf("Z: created=%v err=%v", created, err)
	}
	if rep.SalesCount != 1 || rep.Gross != 700 {
		t.Fatalf("same-second sale must be in this Z: count=%d gross=%d", rep.SalesCount, rep.Gross)
	}
	var cash int64
	for _, m := range rep.Methods {
		if m.Method == "cash" {
			cash = m.In
		}
	}
	if cash != 700 {
		t.Fatalf("cash method In = %d, want 700 (%+v)", cash, rep.Methods)
	}
	if rep.FirstReceipt != "R-5831" || rep.LastReceipt != "R-5831" {
		t.Fatalf("receipt range = %q..%q", rep.FirstReceipt, rep.LastReceipt)
	}
}

func TestGenerateEOD_RowStampedAtBoundLandsInNextZ(t *testing.T) {
	dp := newEODTestDeps(t)
	base := w5831Base()
	T := zCloseBound(base)
	setW5831Clock(t, base)
	w5831CashSale(t, dp, "before", "R-B", 100, T.Add(-time.Second))
	w5831CashSale(t, dp, "atbound", "R-A", 200, T)

	rep, created, err := generateEOD(t.Context(), dp, "system", "", "")
	if err != nil || !created {
		t.Fatalf("Z: created=%v err=%v", created, err)
	}
	if rep.SalesCount != 1 || rep.Gross != 100 {
		t.Fatalf("only the row stamped T-1s is in this Z: count=%d gross=%d", rep.SalesCount, rep.Gross)
	}
	// The next window starts at the archived bound and picks the T row up.
	repo := data.NewPOSRepo(dp.Db)
	from := *mustLatest(t, repo)
	next, err := repo.EndOfDayInstant(t.Context(), from, T.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if next.SalesCount != 1 || next.Gross != 200 {
		t.Fatalf("row stamped T must be in the next Z: count=%d gross=%d", next.SalesCount, next.Gross)
	}
}

func TestGenerateEOD_NextFromEqualsArchivedTo(t *testing.T) {
	dp := newEODTestDeps(t)
	base := w5831Base()
	T := zCloseBound(base)
	setW5831Clock(t, base)

	rep, created, err := generateEOD(t.Context(), dp, "system", "", "")
	if err != nil || !created {
		t.Fatalf("Z: created=%v err=%v", created, err)
	}
	got := mustLatest(t, data.NewPOSRepo(dp.Db))
	if !got.Equal(T) {
		t.Fatalf("LatestArchivedAt = %v, want T = %v", got, T)
	}
	if want := T.Local().Format(time.RFC3339); rep.To != want {
		t.Fatalf("rep.To = %q, want %q", rep.To, want)
	}
	var period string
	if err := dp.Db.QueryRow(`SELECT period FROM report_archive WHERE kind='eod'`).Scan(&period); err != nil {
		t.Fatal(err)
	}
	if period != rep.To {
		t.Fatalf("period = %q, want %q", period, rep.To)
	}
}

func TestGenerateEOD_WaitsForBoundBeforeRelease(t *testing.T) {
	dp := newEODTestDeps(t)
	base := w5831Base()
	T := zCloseBound(base)
	c := setW5831Clock(t, base)

	if _, created, err := generateEOD(t.Context(), dp, "system", "", ""); err != nil || !created {
		t.Fatalf("Z: created=%v err=%v", created, err)
	}
	if len(c.waited) != 1 || !c.waited[0].Equal(T) {
		t.Fatalf("waitUntil calls = %v, want exactly [%v]", c.waited, T)
	}
	if !c.heldAtWait[0] {
		t.Fatal("waitUntil must run while the write lock is still held (before releaseHeld)")
	}

	// A wait that fails (hold deadline) fails the Z itself: nothing archived.
	dp2 := newEODTestDeps(t)
	c2 := setW5831Clock(t, base)
	c2.waitErr = errors.New("hold deadline")
	if _, created, err := generateEOD(t.Context(), dp2, "system", "", ""); err == nil || created {
		t.Fatalf("failed wait must fail the Z: created=%v err=%v", created, err)
	}
	var n int
	if err := dp2.Db.QueryRow(`SELECT COUNT(*) FROM report_archive WHERE kind='eod'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("nothing may be archived after a failed wait: n=%d err=%v", n, err)
	}
}

func TestEODDoneToday_MidnightCloseCountsForPreviousDay(t *testing.T) {
	loc := time.FixedZone("test", 0)
	old := time.Local
	time.Local = loc
	t.Cleanup(func() { time.Local = old })

	d := time.Now().AddDate(0, 0, -200)
	midnight := time.Date(d.Year(), d.Month(), d.Day()+1, 0, 0, 0, 0, loc) // bound of a 23:59:59.x close on day d
	nextDayNoon := midnight.Add(12 * time.Hour)
	prevDayLate := midnight.Add(-time.Minute)

	if eodDoneToday(&midnight, nextDayNoon) {
		t.Fatal("a close at 00:00:00 (23:59:59.x of the day before) must not mark the NEW day done")
	}
	if !eodDoneToday(&midnight, prevDayLate) {
		t.Fatal("that close counts for the previous day")
	}
	noon := midnight.Add(-12 * time.Hour)
	if !eodDoneToday(&noon, noon.Add(time.Hour)) || eodDoneToday(nil, noon) {
		t.Fatal("ordinary same-day close done; nil never done")
	}
}

// Tester (ut-docs#5831): the real waiter returns only once the wall clock has
// reached the target, and gives up with the ctx error (hold deadline) rather
// than sleeping past it.
func TestWaitUntilWall_ReachesTargetOrHonoursCtx(t *testing.T) {
	target := time.Now().Add(30 * time.Millisecond)
	if err := waitUntilWall(t.Context(), target); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if time.Now().Before(target) {
		t.Fatal("returned before the target")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := waitUntilWall(ctx, time.Now().Add(time.Hour))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("ctx not honoured: waited %s", el)
	}
}
