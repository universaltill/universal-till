package data_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/money"
)

// ut-docs#1122: a live basket cleared with New sale before any tender is
// recorded in cleared_baskets (migration 086), so the Z report can show
// how many baskets — and how much money — were cleared before payment.

func TestInsertClearedBasket_WritesRowWithLocalDate(t *testing.T) {
	d, _ := eod1012OpenDB(t)
	repo := data.NewPOSRepo(d.DB)
	at := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	id, err := repo.InsertClearedBasket(context.Background(), data.ClearedBasket{
		CreatedAt: eod1012At(at), RegisterID: "reg-1", ActorID: "u-1", LineCount: 3, Total: money.FromMinor(1250),
	})
	if err != nil || id == "" {
		t.Fatalf("insert: id=%q err=%v", id, err)
	}
	var createdAt, localDate, reg, actor string
	var tillNull bool
	var lines int
	var total int64
	if err := d.DB.QueryRow(`SELECT created_at, local_date, COALESCE(register_id,''), COALESCE(actor_id,''), till_id IS NULL, line_count, total_minor
FROM cleared_baskets WHERE id = ?`, id).Scan(&createdAt, &localDate, &reg, &actor, &tillNull, &lines, &total); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if createdAt != eod1012At(at) || localDate != eod1012Day(t, d, at) || reg != "reg-1" || actor != "u-1" || !tillNull || lines != 3 || total != 1250 {
		t.Fatalf("row = %s %s %s %s till_null=%v %d %d", createdAt, localDate, reg, actor, tillNull, lines, total)
	}
}

func TestInsertClearedBasket_RequiresCreatedAt(t *testing.T) {
	d, _ := eod1012OpenDB(t)
	if _, err := data.NewPOSRepo(d.DB).InsertClearedBasket(context.Background(), data.ClearedBasket{LineCount: 1}); err == nil {
		t.Fatal("insert without CreatedAt must fail")
	}
}

func TestEndOfDay_ClearedBasketsCountedPerDayRangeAndInstant(t *testing.T) {
	d, x := eod1012OpenDB(t)
	repo := data.NewPOSRepo(d.DB)
	ctx := context.Background()

	dayStart := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	day1a := dayStart.Add(10 * time.Hour)
	day1b := dayStart.Add(14 * time.Hour)
	day2 := dayStart.Add(36 * time.Hour)
	for _, c := range []struct {
		at    time.Time
		total int64
	}{{day1a, 500}, {day1b, 799}, {day2, 1000}} {
		if _, err := repo.InsertClearedBasket(ctx, data.ClearedBasket{CreatedAt: eod1012At(c.at), LineCount: 1, Total: money.FromMinor(c.total)}); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	// A completed sale the same day: cleared baskets never touch Gross/Net.
	x(`INSERT INTO sales (id, receipt_no, status, sale_type, subtotal, total, created_at, local_date)
	   VALUES ('s1','R-1','completed','sale',2000,2000,?,date(?, 'localtime'))`, eod1012At(day1a), eod1012At(day1a))

	d1 := eod1012Day(t, d, day1a)
	rep, err := repo.EndOfDay(ctx, d1)
	if err != nil {
		t.Fatalf("eod: %v", err)
	}
	if rep.ClearedBasketCount != 2 || rep.ClearedBasketTotal != 1299 {
		t.Fatalf("day1 cleared = %d / %d, want 2 / 1299", rep.ClearedBasketCount, rep.ClearedBasketTotal)
	}
	if rep.Gross != 2000 || rep.Net != 2000 || rep.CancelCount != 0 || rep.RefundTotal != 0 {
		t.Fatalf("cleared baskets leaked into revenue: gross=%d net=%d cancel=%d refunds=%d", rep.Gross, rep.Net, rep.CancelCount, rep.RefundTotal)
	}

	rng, err := repo.EndOfDayRange(ctx, d1, eod1012Day(t, d, day2))
	if err != nil {
		t.Fatalf("eod range: %v", err)
	}
	if rng.ClearedBasketCount != 3 || rng.ClearedBasketTotal != 2299 {
		t.Fatalf("range cleared = %d / %d, want 3 / 2299", rng.ClearedBasketCount, rng.ClearedBasketTotal)
	}

	// Instant window covering only the second day-1 basket.
	inst, err := repo.EndOfDayInstant(ctx, day1a.Add(time.Hour), day1b.Add(time.Hour))
	if err != nil {
		t.Fatalf("eod instant: %v", err)
	}
	if inst.ClearedBasketCount != 1 || inst.ClearedBasketTotal != 799 {
		t.Fatalf("instant cleared = %d / %d, want 1 / 799", inst.ClearedBasketCount, inst.ClearedBasketTotal)
	}

	empty, err := repo.EndOfDay(ctx, eod1012Day(t, d, dayStart.Add(-24*time.Hour)))
	if err != nil || empty.ClearedBasketCount != 0 || empty.ClearedBasketTotal != 0 {
		t.Fatalf("a day with none = %d / %d (err %v), want 0 / 0", empty.ClearedBasketCount, empty.ClearedBasketTotal, err)
	}
}

// Clear transaction history (ADR-0042) archives cleared_baskets like
// no_sale_events, a post-reset row blocks restore, and restore brings the
// rows back unchanged.
func TestResetThenRestoreRoundTrip_ClearedBaskets(t *testing.T) {
	d, x, count := resetTestDB(t, "restore_cleared_baskets.db")
	seedFullSale(t, x)
	x(`INSERT INTO cleared_baskets (id, created_at, local_date, register_id, till_id, actor_id, line_count, total_minor)
	   VALUES ('cb1','2026-01-01T00:00:00Z','2026-01-01','r1',NULL,'u1',2,450)`)

	repo := data.NewPOSRepo(d.DB)
	ctx := context.Background()
	_, batchID, err := repo.ResetTransactionHistory(ctx, "", "")
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if c := count("cleared_baskets"); c != 0 {
		t.Fatalf("cleared_baskets not cleared by reset: %d", c)
	}
	if c := count("cleared_baskets_archive"); c != 1 {
		t.Fatalf("cleared_baskets_archive holds %d row(s), want 1", c)
	}
	x(`INSERT INTO cleared_baskets (id, created_at, local_date, line_count, total_minor) VALUES ('cb-after','2026-02-01T00:00:00Z','2026-02-01',1,1)`)
	if _, err := repo.RestoreResetBatch(ctx, batchID, "", ""); !errors.Is(err, data.ErrShopHasTradedSinceReset) {
		t.Fatalf("restore with a post-reset cleared basket = %v, want ErrShopHasTradedSinceReset", err)
	}
	x(`DELETE FROM cleared_baskets WHERE id = 'cb-after'`)
	if _, err := repo.RestoreResetBatch(ctx, batchID, "", ""); err != nil {
		t.Fatalf("restore: %v", err)
	}
	var got string
	if err := d.DB.QueryRow(`SELECT id || '|' || created_at || '|' || local_date || '|' || COALESCE(register_id,'-') || '|' ||
	  COALESCE(till_id,'-') || '|' || COALESCE(actor_id,'-') || '|' || line_count || '|' || total_minor FROM cleared_baskets`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if want := "cb1|2026-01-01T00:00:00Z|2026-01-01|r1|-|u1|2|450"; got != want {
		t.Fatalf("restored = %q, want %q", got, want)
	}
}
