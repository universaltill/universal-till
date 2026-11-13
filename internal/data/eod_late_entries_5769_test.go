package data

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/money"
)

// ut-docs#5769: no-sale opens and cleared baskets journaled from another
// till carry an arrival time (received_at); the late-entries note lists what
// a Z counted that was made before its own `from`.

func TestEODInstant_LateEventsCountByReceivedAt(t *testing.T) {
	d := b8OpenDB(t, "late-events.db")
	repo := NewPOSRepo(d.DB)
	ctx := context.Background()
	lateTill(t, d, "till-r", "Replica")
	closeN := iwAnchor()
	prev, next := closeN.Add(-8*time.Hour), closeN.Add(8*time.Hour)

	// made before close N, received after it.
	if _, err := repo.ApplyJournaledNoSaleEvent(ctx, NoSaleEvent{ID: "ns-late", CreatedAt: b8At(closeN.Add(-10 * time.Minute)),
		TillID: "till-r", ActorID: "a", ReceivedAt: b8At(closeN.Add(5 * time.Minute))}); err != nil {
		t.Fatal(err)
	}
	// journaled and received inside Z N.
	if _, err := repo.ApplyJournaledNoSaleEvent(ctx, NoSaleEvent{ID: "ns-ontime", CreatedAt: b8At(closeN.Add(-30 * time.Minute)),
		TillID: "till-r", ReceivedAt: b8At(closeN.Add(-20 * time.Minute))}); err != nil {
		t.Fatal(err)
	}
	// own open: received_at stays NULL, counts by created_at.
	if _, err := repo.InsertNoSaleEvent(ctx, nil, NoSaleEvent{ID: "ns-own", CreatedAt: b8At(closeN.Add(-15 * time.Minute))}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ApplyJournaledClearedBasket(ctx, ClearedBasket{ID: "cb-late", CreatedAt: b8At(closeN.Add(-10 * time.Minute)),
		TillID: "till-r", LineCount: 2, Total: money.FromMinor(250), ReceivedAt: b8At(closeN.Add(6 * time.Minute))}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.InsertClearedBasket(ctx, ClearedBasket{ID: "cb-own", CreatedAt: b8At(closeN.Add(-9 * time.Minute)), LineCount: 1, Total: money.FromMinor(10)}); err != nil {
		t.Fatal(err)
	}
	// pre-migration row: NULL received_at, journaled till, counts by created_at.
	mustExec(t, d, `INSERT INTO no_sale_events (id, created_at, local_date, till_id) VALUES ('ns-old', ?, '', 'till-r')`, b8At(closeN.Add(-40*time.Minute)))

	zN, err := repo.EndOfDayInstant(ctx, prev, closeN)
	if err != nil {
		t.Fatal(err)
	}
	if zN.NoSaleCount != 3 || zN.ClearedBasketCount != 1 || zN.ClearedBasketTotal != 10 {
		t.Fatalf("Z N no-sale=%d cleared=%d/%d, want 3 and 1/10", zN.NoSaleCount, zN.ClearedBasketCount, zN.ClearedBasketTotal)
	}
	if zN.LateEntries != nil {
		t.Fatalf("Z N has nothing late (pre-migration row counts by created_at): %+v", zN.LateEntries)
	}
	zN1, err := repo.EndOfDayInstant(ctx, closeN, next)
	if err != nil {
		t.Fatal(err)
	}
	if zN1.NoSaleCount != 1 || zN1.ClearedBasketCount != 1 || zN1.ClearedBasketTotal != 250 {
		t.Fatalf("Z N+1 no-sale=%d cleared=%d/%d, want 1 and 1/250", zN1.NoSaleCount, zN1.ClearedBasketCount, zN1.ClearedBasketTotal)
	}
	if len(zN1.NoSaleOperators) != 1 {
		t.Fatalf("Z N+1 no-sale operators = %+v, want the one late open", zN1.NoSaleOperators)
	}
}

func TestApplyJournaled_DefaultsReceivedAtToNowAndKeepsFirst(t *testing.T) {
	d := b8OpenDB(t, "late-recv-default.db")
	repo := NewPOSRepo(d.DB)
	ctx := context.Background()
	lateTill(t, d, "till-r", "Replica")
	before := time.Now().Add(-2 * time.Second)
	if _, err := repo.ApplyJournaledNoSaleEvent(ctx, NoSaleEvent{ID: "x", CreatedAt: b8At(before.Add(-time.Hour)), TillID: "till-r"}); err != nil {
		t.Fatal(err)
	}
	first := ""
	if err := d.QueryRow(`SELECT COALESCE(received_at,'') FROM no_sale_events WHERE id='x'`).Scan(&first); err != nil || first == "" {
		t.Fatalf("received_at = %q err=%v, want stamped", first, err)
	}
	if got, _ := time.Parse(time.RFC3339, first); got.Before(before) {
		t.Fatalf("received_at %q is not ~now", first)
	}
	// a re-send keeps the first arrival time.
	if applied, err := repo.ApplyJournaledNoSaleEvent(ctx, NoSaleEvent{ID: "x", CreatedAt: b8At(before.Add(-time.Hour)), TillID: "till-r", ReceivedAt: "2099-01-01T00:00:00Z"}); err != nil || applied {
		t.Fatalf("re-send applied=%v err=%v", applied, err)
	}
	var again string
	_ = d.QueryRow(`SELECT received_at FROM no_sale_events WHERE id='x'`).Scan(&again)
	if again != first {
		t.Fatalf("re-send changed received_at %q -> %q", first, again)
	}
	// local inserts stay NULL.
	if _, err := repo.InsertNoSaleEvent(ctx, nil, NoSaleEvent{ID: "own", CreatedAt: b8At(before)}); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = d.QueryRow(`SELECT COUNT(*) FROM no_sale_events WHERE id='own' AND received_at IS NULL`).Scan(&n)
	if n != 1 {
		t.Fatal("a local no-sale must leave received_at NULL")
	}
}

func TestLateEntriesForInstantWindow(t *testing.T) {
	d := b8OpenDB(t, "late-entries.db")
	repo := NewPOSRepo(d.DB)
	ctx := context.Background()
	lateTill(t, d, "till-r", "Replica")
	lateTill(t, d, "till-q", "Queue")
	closeN := iwAnchor()
	next := closeN.Add(8 * time.Hour)

	// Nothing late: nil. First-ever close (zero from): nil even with old rows.
	if le, err := repo.LateEntriesForInstantWindow(ctx, closeN, next); err != nil || le != nil {
		t.Fatalf("empty = %+v err=%v, want nil", le, err)
	}
	lateSale(t, d, "l1", "till-r", b8At(closeN.Add(-3*time.Hour)), b8At(closeN.Add(time.Minute)), 500)
	lateSale(t, d, "l2", "till-r", b8At(closeN.Add(-2*time.Hour)), b8At(closeN.Add(2*time.Minute)), 250)
	lateSale(t, d, "l3", "till-q", b8At(closeN.Add(-time.Hour)), b8At(closeN.Add(3*time.Minute)), 100)
	// a refund from a till is a late refund, not a late sale.
	mustExec(t, d, `INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at, completed_at, till_id, local_date)
VALUES ('lr', 'R-lr', 'completed', 'return', 'GBP', 40, 0, 0, 40, ?, ?, 'till-q', '')`, b8At(closeN.Add(-90*time.Minute)), b8At(closeN.Add(4*time.Minute)))
	// on time journaled (created inside the window): not late.
	lateSale(t, d, "ok", "till-r", b8At(closeN.Add(time.Hour)), b8At(closeN.Add(2*time.Hour)), 999)
	// own sale: never late.
	lateSale(t, d, "own", "", b8At(closeN.Add(time.Minute)), b8At(closeN.Add(time.Minute)), 1)
	if _, err := repo.ApplyJournaledNoSaleEvent(ctx, NoSaleEvent{ID: "n1", CreatedAt: b8At(closeN.Add(-4 * time.Hour)), TillID: "till-r", ReceivedAt: b8At(closeN.Add(time.Minute))}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ApplyJournaledClearedBasket(ctx, ClearedBasket{ID: "c1", CreatedAt: b8At(closeN.Add(-time.Hour)), TillID: "till-q", ReceivedAt: b8At(closeN.Add(time.Minute))}); err != nil {
		t.Fatal(err)
	}

	le, err := repo.LateEntriesForInstantWindow(ctx, closeN, next)
	if err != nil || le == nil {
		t.Fatalf("late = %+v err=%v", le, err)
	}
	if len(le.Tills) != 2 {
		t.Fatalf("tills = %+v, want 2", le.Tills)
	}
	byID := map[string]EODLateTill{}
	for _, tl := range le.Tills {
		byID[tl.TillID] = tl
	}
	r, q := byID["till-r"], byID["till-q"]
	if r.Name != "Replica" || r.SalesCount != 2 || r.Gross != 750 || r.NoSaleCount != 1 || r.ClearedBasketCount != 0 || r.RefundCount != 0 {
		t.Fatalf("till-r = %+v", r)
	}
	if q.Name != "Queue" || q.SalesCount != 1 || q.Gross != 100 || q.RefundCount != 1 || q.RefundTotal != 40 || q.ClearedBasketCount != 1 || q.NoSaleCount != 0 {
		t.Fatalf("till-q = %+v", q)
	}
	want := closeN.Add(-4 * time.Hour).Local().Format(time.RFC3339)
	if le.EarliestOrigin != want {
		t.Fatalf("EarliestOrigin = %q, want %q (local offset)", le.EarliestOrigin, want)
	}
	if le, err := repo.LateEntriesForInstantWindow(ctx, time.Time{}, next); err != nil || le != nil {
		t.Fatalf("first-ever close must have no late entries, got %+v err=%v", le, err)
	}
	// The note rides on the report.
	rep, err := repo.EndOfDayInstant(ctx, closeN, next)
	if err != nil || rep.LateEntries == nil || len(rep.LateEntries.Tills) != 2 {
		t.Fatalf("EODReport.LateEntries = %+v err=%v", rep.LateEntries, err)
	}
}

// A close that takes `to` and reads its streams under HoldWriteLock sees every
// concurrent apply in exactly one Z: stamped before `to` => committed and
// visible; otherwise stamped at/after `to` => the next Z.
func TestEODClose_AtomicAgainstConcurrentApply(t *testing.T) {
	d := b8OpenDB(t, "late-atomic.db")
	repo := NewPOSRepo(d.DB)
	ctx := context.Background()
	lateTill(t, d, "till-r", "Replica")
	const n = 150
	origin := b8At(time.Now().Add(-1000 * time.Hour))

	done := make(chan error, 1)
	go func() {
		for i := 0; i < n; i++ {
			_, err := repo.ApplyJournaledNoSaleEvent(ctx, NoSaleEvent{ID: fmt.Sprintf("ev-%03d", i), CreatedAt: origin, TillID: "till-r"})
			if err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()

	total := 0
	prev := time.Now().Add(-2000 * time.Hour)
	closeOnce := func(final bool) {
		release, err := repo.HoldWriteLock(ctx)
		if err != nil {
			t.Fatal(err)
		}
		to := time.Now()
		if final {
			to = to.Add(2 * time.Second)
		}
		rep, err := repo.EndOfDayInstant(ctx, prev, to)
		release()
		if err != nil {
			t.Fatal(err)
		}
		total += rep.NoSaleCount
		prev = to
	}
	for running := true; running; {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			running = false
		default:
			closeOnce(false)
			time.Sleep(3 * time.Millisecond)
		}
	}
	closeOnce(true)
	if total != n {
		t.Fatalf("rows counted across the Zs = %d, want exactly %d (each in exactly one Z)", total, n)
	}
}
