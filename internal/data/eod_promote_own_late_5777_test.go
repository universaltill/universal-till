package data

import (
	"context"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/db"
)

// ut-docs#5777: a promoted till's own sales that the previous main till
// recorded after its last close reach the till's next Z by that recording
// time (ADR-0066 amendment rule 1), kept in sales.recorded_at.

const own5777Fmt = "2006-01-02 15:04:05"

// own5777Arrival feeds one own-origin copied record (completed_at = at, ""
// for none) through the real apply path, as the pull does.
func own5777Arrival(t *testing.T, r *SalesMirrorRepo, seq int64, id, at string) {
	t.Helper()
	s := testMirrorSale(id, "till-self", 100)
	s.LocalDate = time.Now().Format("2006-01-02")
	if at != "" {
		s.CompletedAt = &at
	}
	if _, err := r.ApplyFeedPage(context.Background(), MirrorFeedPage{Epoch: "e1", FromSeq: seq - 1, ToSeq: seq,
		Records: []MirrorRecord{saleRec(seq, s)}}, MirrorApplyOpts{SelfTillID: "till-self", Now: time.Now()}); err != nil {
		t.Fatal(err)
	}
}

// own5777 is the scene: own sale S made at t1 (before the old main till's
// close at floor), applied there at t3 (after it).
type own5777 struct {
	repo       *POSRepo
	mirror     *SalesMirrorRepo
	d          *db.DB
	t1, fl, t3 time.Time
	floorS     string
}

func newOwn5777(t *testing.T, name string) (*own5777, func(q string, a ...any) string) {
	t.Helper()
	d := b8OpenDB(t, name)
	now := time.Now()
	fx := &own5777{d: d, repo: NewPOSRepo(d.DB), mirror: NewSalesMirrorRepo(d.DB),
		t1: now.Add(-5 * time.Hour), fl: now.Add(-3 * time.Hour), t3: now.Add(-1 * time.Hour)}
	fx.floorS = fx.fl.UTC().Format(own5777Fmt)
	startCopy(t, fx.mirror, "e1", 0)
	lateSale(t, d, "S", "", b8At(fx.t1), b8At(fx.t1), 700)
	own5777Arrival(t, fx.mirror, 1, "S", b8At(fx.t3))
	scalar := func(q string, a ...any) string {
		var v string
		if err := d.QueryRow(q, a...).Scan(&v); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return v
	}
	return fx, scalar
}

func TestApplyMirror_OwnSaleKeepsArrivalNotCopy_5777(t *testing.T) {
	sqlDB := openMirrorRepoDB(t)
	r := NewSalesMirrorRepo(sqlDB)
	startCopy(t, r, "e1", 0)
	at1 := b8At(time.Now().Add(-2 * time.Hour))
	at2 := b8At(time.Now().Add(-1 * time.Hour))
	own5777Arrival(t, r, 1, "s-own", at1)
	if n := mirrorCount(t, sqlDB, "mirror_sales"); n != 0 {
		t.Fatalf("own sale stored in the copy: %d rows", n)
	}
	var at string
	if err := sqlDB.QueryRow(`SELECT completed_at FROM mirror_sale_arrivals WHERE id = 's-own'`).Scan(&at); err != nil || at != at1 {
		t.Fatalf("arrival = %q err %v, want %q", at, err, at1)
	}
	own5777Arrival(t, r, 2, "s-own", at2) // a newer apply of the record replaces the time
	if err := sqlDB.QueryRow(`SELECT completed_at FROM mirror_sale_arrivals WHERE id = 's-own'`).Scan(&at); err != nil || at != at2 {
		t.Fatalf("arrival after re-apply = %q err %v, want %q", at, err, at2)
	}
	// A window-filtered record and one without completed_at keep no arrival.
	old := testMirrorSale("s-old", "till-self", 1)
	old.LocalDate = "2000-01-01"
	old.CompletedAt = &at2
	if _, err := r.ApplyFeedPage(context.Background(), MirrorFeedPage{Epoch: "e1", FromSeq: 2, ToSeq: 3,
		Records: []MirrorRecord{saleRec(3, old)}}, MirrorApplyOpts{SelfTillID: "till-self", WindowStart: "2025-01-01", Now: time.Now()}); err != nil {
		t.Fatal(err)
	}
	own5777Arrival(t, r, 4, "s-null", "")
	if n := mirrorCount(t, sqlDB, "mirror_sale_arrivals"); n != 1 {
		t.Fatalf("arrivals = %d, want 1 (filtered and empty ones kept out)", n)
	}
	// Pruning the copy drops arrivals older than the sales window.
	if _, err := sqlDB.Exec(`UPDATE mirror_sale_arrivals SET local_date = '2000-01-01'`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.PruneCopyBefore(context.Background(), "2001-01-01", ""); err != nil {
		t.Fatal(err)
	}
	if n := mirrorCount(t, sqlDB, "mirror_sale_arrivals"); n != 0 {
		t.Fatalf("arrivals after prune = %d, want 0", n)
	}
}

func TestMarkOwnLate_CountsOnceInNextZ_5777(t *testing.T) {
	fx, scalar := newOwn5777(t, "own-late.db")
	ctx := context.Background()
	before := scalar(`SELECT created_at||'|'||local_date||'|'||completed_at||'|'||receipt_no FROM sales WHERE id='S'`)
	n, err := fx.mirror.MarkOwnLateArrivals(ctx, fx.floorS, "")
	if err != nil || n != 1 {
		t.Fatalf("MarkOwnLateArrivals = %d, %v; want 1", n, err)
	}
	if after := scalar(`SELECT created_at||'|'||local_date||'|'||completed_at||'|'||receipt_no FROM sales WHERE id='S'`); after != before {
		t.Fatalf("origin fields moved: %q -> %q", before, after)
	}
	next := time.Now().Add(time.Hour)
	z, err := fx.repo.EndOfDayInstant(ctx, fx.fl, next)
	if err != nil {
		t.Fatal(err)
	}
	if z.SalesCount != 1 || z.Gross != 700 || len(z.Methods) != 1 || z.Methods[0].In != 700 {
		t.Fatalf("next Z = %d/%d methods %+v, want 1/700 cash 700", z.SalesCount, z.Gross, z.Methods)
	}
	if bands, err := fx.repo.SalesForTaxBandsInstant(ctx, fx.fl, next); err != nil || len(bands) != 1 {
		t.Fatalf("tax band sales in next Z = %+v err %v, want [S]", bands, err)
	}
	// The window ending at the floor (the old main till's Z) does not hold it.
	zOld, err := fx.repo.EndOfDayInstant(ctx, fx.fl.Add(-6*time.Hour), fx.fl)
	if err != nil {
		t.Fatal(err)
	}
	if zOld.SalesCount != 0 {
		t.Fatalf("the window before the floor counts %d sales, want 0", zOld.SalesCount)
	}
}

func TestMarkOwnLate_LateNoteOwnTill_5777(t *testing.T) {
	fx, scalar := newOwn5777(t, "own-late-note.db")
	ctx := context.Background()
	mustExec(t, fx.d, `INSERT INTO settings (key, value) VALUES ('till_identity.cloud_id', 'till-self'), ('till.name', 'Back counter')`)
	if _, err := fx.mirror.MarkOwnLateArrivals(ctx, fx.floorS, ""); err != nil {
		t.Fatal(err)
	}
	late, err := fx.repo.LateEntriesForInstantWindow(ctx, fx.fl, time.Now().Add(time.Hour))
	if err != nil || late == nil || len(late.Tills) != 1 {
		t.Fatalf("late entries = %+v err %v, want one till", late, err)
	}
	e := late.Tills[0]
	if e.TillID != "till-self" || e.Name != "Back counter" || e.SalesCount != 1 || e.Gross != 700 {
		t.Fatalf("entry = %+v, want own till 1 sale 700", e)
	}
	wantEarliest := fx.t1.UTC().Truncate(time.Second)
	if got, perr := time.Parse(time.RFC3339, late.EarliestOrigin); perr != nil || !got.UTC().Equal(wantEarliest) {
		t.Fatalf("earliest_origin = %q (%v), want %s", late.EarliestOrigin, perr, wantEarliest)
	}
	_ = scalar
}

func TestMarkOwnLate_AppliedBeforeClose_NotMarked_5777(t *testing.T) {
	fx, scalar := newOwn5777(t, "own-late-before.db")
	// applied on the old main till BEFORE its close: its Z counted it.
	mustExec(t, fx.d, `UPDATE mirror_sale_arrivals SET completed_at = ?`, b8At(fx.fl.Add(-time.Minute)))
	if n, err := fx.mirror.MarkOwnLateArrivals(context.Background(), fx.floorS, ""); err != nil || n != 0 {
		t.Fatalf("marked %d (%v), want 0", n, err)
	}
	if v := scalar(`SELECT COALESCE(recorded_at,'') FROM sales WHERE id='S'`); v != "" {
		t.Fatalf("recorded_at = %q, want NULL", v)
	}
}

func TestMarkOwnLate_BeforeOwnLastClose_NotMarked_5777(t *testing.T) {
	fx, _ := newOwn5777(t, "own-late-ownclose.db")
	own := fx.t1.Add(time.Hour).UTC().Format(own5777Fmt) // this till's own Z closed after S was made
	if n, err := fx.mirror.MarkOwnLateArrivals(context.Background(), fx.floorS, own); err != nil || n != 0 {
		t.Fatalf("marked %d (%v), want 0 (this till's own Z already counted it)", n, err)
	}
}

func TestMarkOwnLate_Rerun_NoOp_5777(t *testing.T) {
	fx, scalar := newOwn5777(t, "own-late-rerun.db")
	ctx := context.Background()
	if n, err := fx.mirror.MarkOwnLateArrivals(ctx, fx.floorS, ""); err != nil || n != 1 {
		t.Fatalf("first = %d, %v", n, err)
	}
	first := scalar(`SELECT recorded_at FROM sales WHERE id='S'`)
	// A later arrival time must not move it to a later Z.
	mustExec(t, fx.d, `UPDATE mirror_sale_arrivals SET completed_at = ?`, b8At(time.Now()))
	if n, err := fx.mirror.MarkOwnLateArrivals(ctx, fx.floorS, ""); err != nil || n != 0 {
		t.Fatalf("second = %d, %v; want 0", n, err)
	}
	if got := scalar(`SELECT recorded_at FROM sales WHERE id='S'`); got != first {
		t.Fatalf("recorded_at moved %q -> %q", first, got)
	}
}

func TestMarkOwnLate_NullCompletedAt_5777(t *testing.T) {
	d := b8OpenDB(t, "own-late-null.db")
	r := NewSalesMirrorRepo(d.DB)
	startCopy(t, r, "e1", 0)
	lateSale(t, d, "S", "", b8At(time.Now().Add(-5*time.Hour)), b8At(time.Now().Add(-5*time.Hour)), 700)
	own5777Arrival(t, r, 1, "S", "")
	floor := time.Now().Add(-3 * time.Hour).UTC().Format(own5777Fmt)
	if n, err := r.MarkOwnLateArrivals(context.Background(), floor, ""); err != nil || n != 0 {
		t.Fatalf("marked %d (%v), want 0: no arrival time means not late", n, err)
	}
}

func TestMarkOwnLate_OutsideAnyZ_Disjoint_5777(t *testing.T) {
	fx, _ := newOwn5777(t, "own-late-disjoint.db")
	ctx := context.Background()
	// U: own, never reached the main till (no arrival), also before the floor.
	lateSale(t, fx.d, "U", "", b8At(fx.t1.Add(time.Minute)), b8At(fx.t1.Add(time.Minute)), 50)
	if n, err := fx.mirror.MarkOwnLateArrivals(ctx, fx.floorS, ""); err != nil || n != 1 {
		t.Fatalf("marked %d (%v), want 1", n, err)
	}
	own, _, err := fx.mirror.SalesOutsideAnyZ(ctx, fx.floorS, "", "")
	if err != nil || own != 1 {
		t.Fatalf("own outside Z = %d (%v), want 1: only the never-pushed U", own, err)
	}
}

// A late sale from ANOTHER till that is missing from the roster (or has no
// name) keeps its pre-#5777 label "": only this till's own rows (empty
// till_id) fall back to its own till.name.
func TestLateNote_OtherTillNotLabelledWithOwnName_5777(t *testing.T) {
	d := b8OpenDB(t, "own-late-foreign.db")
	repo := NewPOSRepo(d.DB)
	now := time.Now()
	mustExec(t, d, `INSERT INTO settings (key, value) VALUES ('till_identity.cloud_id', 'till-self'), ('till.name', 'Back counter')`)
	lateTill(t, d, "till-noname", "")
	lateSale(t, d, "F1", "till-gone", b8At(now.Add(-5*time.Hour)), b8At(now.Add(-time.Hour)), 300)   // not in the roster
	lateSale(t, d, "F2", "till-noname", b8At(now.Add(-5*time.Hour)), b8At(now.Add(-time.Hour)), 400) // empty name
	late, err := repo.LateEntriesForInstantWindow(context.Background(), now.Add(-3*time.Hour), now.Add(time.Hour))
	if err != nil || late == nil || len(late.Tills) != 2 {
		t.Fatalf("late entries = %+v err %v, want two tills", late, err)
	}
	for _, e := range late.Tills {
		if e.Name != "" {
			t.Fatalf("till %s labelled %q, want \"\" (not this till's own name)", e.TillID, e.Name)
		}
	}
}

func TestClearOwnArrivals_5777(t *testing.T) {
	fx, _ := newOwn5777(t, "own-late-clear.db")
	if err := fx.mirror.ClearOwnArrivals(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := mirrorCount(t, fx.d.DB, "mirror_sale_arrivals"); n != 0 {
		t.Fatalf("arrivals after clear = %d, want 0", n)
	}
}
