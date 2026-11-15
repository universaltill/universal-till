package data

import (
	"context"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/money"
)

// Tester additions for ut-docs#5769 (ADR-0066 amendment).

// AC2: three consecutive closes, on-time and late rows of every journaled
// stream mixed with own rows. A row made before close N and applied after it
// is in Z N+1 only (not Z N, not Z N+2); a row applied after close N+1 with
// an origin before close N is in Z N+2 only; summing every Z gives every row
// exactly once.
func TestEODInstant_ThreeCloses_EachRowInExactlyOneZ(t *testing.T) {
	d := b8OpenDB(t, "late-three.db")
	repo := NewPOSRepo(d.DB)
	ctx := context.Background()
	lateTill(t, d, "till-r", "Replica")

	c0 := iwAnchor().Add(-24 * time.Hour) // close N-1 (prev)
	c1 := c0.Add(8 * time.Hour)           // close N
	c2 := c1.Add(8 * time.Hour)           // close N+1
	c3 := c2.Add(8 * time.Hour)           // close N+2

	// own sales (created == completed), one per window.
	lateSale(t, d, "own-1", "", b8At(c0.Add(time.Hour)), b8At(c0.Add(time.Hour)), 1)
	lateSale(t, d, "own-2", "", b8At(c1.Add(time.Hour)), b8At(c1.Add(time.Hour)), 2)
	lateSale(t, d, "own-3", "", b8At(c2.Add(time.Hour)), b8At(c2.Add(time.Hour)), 4)
	// journaled on time in Z N.
	lateSale(t, d, "j-ontime", "till-r", b8At(c0.Add(2*time.Hour)), b8At(c0.Add(3*time.Hour)), 8)
	// made before close N, applied after it: Z N+1 only.
	lateSale(t, d, "j-late1", "till-r", b8At(c1.Add(-10*time.Minute)), b8At(c1.Add(5*time.Minute)), 16)
	// made before close N, applied after close N+1: Z N+2 only.
	lateSale(t, d, "j-late2", "till-r", b8At(c1.Add(-20*time.Minute)), b8At(c2.Add(5*time.Minute)), 32)

	ns := func(id string, created, recv time.Time) {
		if _, err := repo.ApplyJournaledNoSaleEvent(ctx, NoSaleEvent{ID: id, CreatedAt: b8At(created), TillID: "till-r", ReceivedAt: b8At(recv)}); err != nil {
			t.Fatal(err)
		}
	}
	cb := func(id string, created, recv time.Time, total int64) {
		if _, err := repo.ApplyJournaledClearedBasket(ctx, ClearedBasket{ID: id, CreatedAt: b8At(created), TillID: "till-r", LineCount: 1,
			Total: money.FromMinor(total), ReceivedAt: b8At(recv)}); err != nil {
			t.Fatal(err)
		}
	}
	ns("ns-ontime", c0.Add(time.Hour), c0.Add(2*time.Hour))
	ns("ns-late1", c1.Add(-time.Minute), c1.Add(time.Minute))
	ns("ns-late2", c1.Add(-2*time.Minute), c2.Add(time.Minute))
	cb("cb-late1", c1.Add(-time.Minute), c1.Add(time.Minute), 100)
	cb("cb-late2", c1.Add(-2*time.Minute), c2.Add(time.Minute), 200)

	type z struct {
		sales, gross, ns, cb, cbTotal int64
		late                          *EODLateEntries
	}
	run := func(from, to time.Time) z {
		rep, err := repo.EndOfDayInstant(ctx, from, to)
		if err != nil {
			t.Fatal(err)
		}
		return z{int64(rep.SalesCount), rep.Gross, int64(rep.NoSaleCount), int64(rep.ClearedBasketCount), rep.ClearedBasketTotal, rep.LateEntries}
	}
	zN, zN1, zN2 := run(c0, c1), run(c1, c2), run(c2, c3)

	if zN.gross != 1+8 || zN.ns != 1 || zN.cb != 0 || zN.late != nil {
		t.Fatalf("Z N = %+v, want gross 9 (own-1 + j-ontime), 1 no-sale, no late note", zN)
	}
	if zN1.gross != 2+16 || zN1.ns != 1 || zN1.cb != 1 || zN1.cbTotal != 100 {
		t.Fatalf("Z N+1 = %+v, want gross 18 (own-2 + j-late1), 1 no-sale, cleared 1/100", zN1)
	}
	if zN2.gross != 4+32 || zN2.ns != 1 || zN2.cb != 1 || zN2.cbTotal != 200 {
		t.Fatalf("Z N+2 = %+v, want gross 36 (own-3 + j-late2), 1 no-sale, cleared 1/200", zN2)
	}
	if sum := zN.gross + zN1.gross + zN2.gross; sum != 63 {
		t.Fatalf("sum of Z gross = %d, want 63 (every row exactly once)", sum)
	}
	if s := zN.sales + zN1.sales + zN2.sales; s != 6 {
		t.Fatalf("sum of Z sales = %d, want 6", s)
	}
	// Late notes: Z N+1 lists j-late1 (its own late rows only); Z N+2 lists
	// j-late2 (origin before Z N+2's from = c2).
	if zN1.late == nil || len(zN1.late.Tills) != 1 || zN1.late.Tills[0].SalesCount != 1 || zN1.late.Tills[0].Gross != 16 ||
		zN1.late.Tills[0].NoSaleCount != 1 || zN1.late.Tills[0].ClearedBasketCount != 1 {
		t.Fatalf("Z N+1 late = %+v", zN1.late)
	}
	if zN2.late == nil || len(zN2.late.Tills) != 1 || zN2.late.Tills[0].SalesCount != 1 || zN2.late.Tills[0].Gross != 32 {
		t.Fatalf("Z N+2 late = %+v", zN2.late)
	}
	if want := c1.Add(-20 * time.Minute).Truncate(time.Second).Local().Format(time.RFC3339); zN2.late.EarliestOrigin != want {
		t.Fatalf("Z N+2 earliest origin = %q, want %q", zN2.late.EarliestOrigin, want)
	}
}

// AC8 (data level): after a promote, generateEOD's `from` is the previous
// main till's last close (eodWindowFloor). Copied sales keep the previous
// main till's completed_at (its apply time, served from its own sales
// table). A copied sale recorded there before the floor (in its Z) is not
// counted again; one made before the floor but recorded after it (in no
// previous Z) is counted once and flagged late; the next Z does not repeat it.
func TestEODInstant_PromoteFloor_CopiedSalesCountedAtMostOnce(t *testing.T) {
	d := b8OpenDB(t, "late-promote.db")
	repo := NewPOSRepo(d.DB)
	ctx := context.Background()
	lateTill(t, d, "till-c", "Third till")
	floor := iwAnchor()
	next := floor.Add(8 * time.Hour)

	// counted by the old main till's last Z (recorded before its close).
	lateSale(t, d, "cp-inz", "reg-old-main", b8At(floor.Add(-2*time.Hour)), b8At(floor.Add(-2*time.Hour)), 10)
	lateSale(t, d, "cp-inz-j", "till-c", b8At(floor.Add(-3*time.Hour)), b8At(floor.Add(-time.Hour)), 20)
	// made before the floor on till-c, recorded by the old main after its close.
	lateSale(t, d, "cp-late", "till-c", b8At(floor.Add(-30*time.Minute)), b8At(floor.Add(10*time.Minute)), 40)

	z1, err := repo.EndOfDayInstant(ctx, floor, next)
	if err != nil {
		t.Fatal(err)
	}
	if z1.SalesCount != 1 || z1.Gross != 40 {
		t.Fatalf("first Z after promote = %d/%d, want 1/40 (only the copied sale no previous Z counted)", z1.SalesCount, z1.Gross)
	}
	if z1.LateEntries == nil || len(z1.LateEntries.Tills) != 1 || z1.LateEntries.Tills[0].TillID != "till-c" || z1.LateEntries.Tills[0].Gross != 40 {
		t.Fatalf("late note = %+v, want till-c 40", z1.LateEntries)
	}
	z2, err := repo.EndOfDayInstant(ctx, next, next.Add(8*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if z2.SalesCount != 0 {
		t.Fatalf("next Z re-counted %d copied sales", z2.SalesCount)
	}
}
