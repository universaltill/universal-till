package data

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/money"
)

// ut-docs#4721: baskets cleared with New sale (cleared_baskets) roll up into
// the cloud sales aggregate's cleared_basket_count / cleared_basket_total_minor
// under the same (business date, till) key as no-sale opens.

func clearedAt(t *testing.T, repo *POSRepo, at time.Time, tillID, registerID string, lines int, total int64) {
	t.Helper()
	if _, err := repo.InsertClearedBasket(context.Background(), ClearedBasket{
		CreatedAt: b8At(at), TillID: tillID, RegisterID: registerID, ActorID: "u1",
		LineCount: lines, Total: money.FromMinor(total),
	}); err != nil {
		t.Fatalf("InsertClearedBasket: %v", err)
	}
}

func TestClearedBasketsForTill_ScopedByDayAndTill(t *testing.T) {
	f := seedSalesAggregateFixture(t)
	repo := NewPOSRepo(f.d.DB)
	ctx := context.Background()

	clearedAt(t, repo, f.today, "", "reg-A", 2, 500)
	clearedAt(t, repo, f.today.Add(time.Hour), "", "reg-A", 1, 250)
	clearedAt(t, repo, f.today, "", "reg-B", 1, 999)         // other till
	clearedAt(t, repo, f.yesterday, "", "reg-A", 1, 777)     // other day
	clearedAt(t, repo, f.today, "replica-1", "reg-A", 3, 40) // till_id wins over register_id
	clearedAt(t, repo, f.today, "", "", 1, 60)               // neither -> selfTill

	for _, tc := range []struct {
		day, till string
		n         int
		total     int64
	}{
		{f.day, "reg-A", 2, 750},
		{f.day, "reg-B", 1, 999},
		{f.prevDay, "reg-A", 1, 777},
		{f.day, "replica-1", 1, 40},
		{f.day, "self-till", 1, 60},
		{f.prevDay, "reg-B", 0, 0},
	} {
		n, total, err := repo.ClearedBasketsForTill(ctx, tc.day, tc.till, "self-till")
		if err != nil {
			t.Fatalf("ClearedBasketsForTill: %v", err)
		}
		if n != tc.n || total.Minor() != tc.total {
			t.Fatalf("%s/%s = %d/%d, want %d/%d", tc.day, tc.till, n, total.Minor(), tc.n, tc.total)
		}
	}

	agg, err := repo.SalesAggregateForTill(ctx, f.day, "reg-A", "self-till", false)
	if err != nil {
		t.Fatal(err)
	}
	if agg.ClearedBasketCount != 2 || agg.ClearedBasketTotal.Minor() != 750 {
		t.Fatalf("aggregate cleared = %d/%d, want 2/750", agg.ClearedBasketCount, agg.ClearedBasketTotal.Minor())
	}
}

func TestSalesAggregateKeys_IncludesClearedOnlyDayAndTill(t *testing.T) {
	d := b8OpenDB(t, "cleared-keys.db")
	repo := NewPOSRepo(d.DB)
	ctx := context.Background()
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.Local)
	day := b8ExpectedDay(t, d, at, 0, 0)
	clearedAt(t, repo, at, "", "", 1, 100)
	clearedAt(t, repo, at, "", "reg-Z", 1, 100)
	clearedAt(t, repo, at.AddDate(0, 0, -30), "", "reg-Z", 1, 100) // outside the window

	keys, err := repo.SalesAggregateKeys(ctx, day, day, "self-till")
	if err != nil {
		t.Fatalf("SalesAggregateKeys: %v", err)
	}
	want := []SalesAggregateKey{{BusinessDate: day, TillID: "reg-Z"}, {BusinessDate: day, TillID: "self-till"}}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("keys = %+v, want %+v", keys, want)
	}
	fps, err := repo.SalesAggregateFingerprints(ctx, day, day, "self-till")
	if err != nil {
		t.Fatal(err)
	}
	if len(fps) != len(want) {
		t.Fatalf("fingerprint keys = %+v, want %d", fps, len(want))
	}
}
