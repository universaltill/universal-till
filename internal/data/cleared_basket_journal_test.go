package data_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/money"
)

// ut-docs#4711: a replica journals its own cleared baskets to the main till
// (LAN sync D3, POST /api/sync/cleared-baskets), as for no-sale opens
// (ut-docs#3562), so the main till's Z report counts every till's clears.

func TestLocalClearedBasketsSince_OwnRowsOrderedByCompositeCursor(t *testing.T) {
	d, _ := eod1012OpenDB(t)
	repo := data.NewPOSRepo(d.DB)
	ctx := context.Background()
	ins := func(id, at, till string) {
		t.Helper()
		if _, err := repo.InsertClearedBasket(ctx, data.ClearedBasket{
			ID: id, CreatedAt: at, TillID: till, RegisterID: "reg-A", ActorID: "u1", LineCount: 2, Total: money.FromMinor(500),
		}); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}
	// Two clears in the SAME second, one later, one earlier, and a journaled
	// row from another till that must never be re-pushed.
	ins("cb-c", "2026-09-01T10:00:00Z", "")
	ins("cb-b", "2026-09-01T10:00:00Z", "")
	ins("cb-d", "2026-09-01T11:00:00Z", "")
	ins("cb-a", "2026-09-01T09:00:00Z", "")
	ins("cb-remote", "2026-09-01T10:30:00Z", "till-other")

	ids := func(bs []data.ClearedBasket) []string {
		out := make([]string, 0, len(bs))
		for _, b := range bs {
			out = append(out, b.ID)
		}
		return out
	}
	all, err := repo.LocalClearedBasketsSince(ctx, "", "", 50)
	if err != nil {
		t.Fatalf("LocalClearedBasketsSince: %v", err)
	}
	if got, want := ids(all), []string{"cb-a", "cb-b", "cb-c", "cb-d"}; !slices.Equal(got, want) {
		t.Fatalf("from the start = %v, want %v (own rows only, ordered by (created_at, id))", got, want)
	}
	if b := all[1]; b.RegisterID != "reg-A" || b.ActorID != "u1" || b.LineCount != 2 || b.Total.Minor() != 500 || b.CreatedAt != "2026-09-01T10:00:00Z" {
		t.Fatalf("row fields not read back: %+v", b)
	}
	next, err := repo.LocalClearedBasketsSince(ctx, "2026-09-01T10:00:00Z", "cb-b", 50)
	if err != nil {
		t.Fatalf("LocalClearedBasketsSince(cursor): %v", err)
	}
	if got, want := ids(next), []string{"cb-c", "cb-d"}; !slices.Equal(got, want) {
		t.Fatalf("after (10:00, cb-b) = %v, want %v", got, want)
	}
	limited, err := repo.LocalClearedBasketsSince(ctx, "", "", 2)
	if err != nil {
		t.Fatalf("LocalClearedBasketsSince(limit): %v", err)
	}
	if got, want := ids(limited), []string{"cb-a", "cb-b"}; !slices.Equal(got, want) {
		t.Fatalf("limit 2 = %v, want %v", got, want)
	}
}

func TestApplyJournaledClearedBasket_IdempotentAndStoresTill(t *testing.T) {
	d, _ := eod1012OpenDB(t)
	repo := data.NewPOSRepo(d.DB)
	ctx := context.Background()
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	e := data.ClearedBasket{
		ID: "cb-remote-1", CreatedAt: eod1012At(at), RegisterID: "reg-R", TillID: "till-7",
		ActorID: "u1", LineCount: 4, Total: money.FromMinor(1999),
	}
	applied, err := repo.ApplyJournaledClearedBasket(ctx, e)
	if err != nil || !applied {
		t.Fatalf("first apply = (%v, %v), want (true, nil)", applied, err)
	}
	applied, err = repo.ApplyJournaledClearedBasket(ctx, e)
	if err != nil || applied {
		t.Fatalf("second apply = (%v, %v), want (false, nil) — idempotent by id", applied, err)
	}
	var n, lines int
	var total int64
	var till, localDate, reg, actor string
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM cleared_baskets WHERE id = 'cb-remote-1'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("rows = %d (%v), want 1", n, err)
	}
	if err := d.DB.QueryRow(`SELECT till_id, local_date, register_id, actor_id, line_count, total_minor FROM cleared_baskets WHERE id = 'cb-remote-1'`).
		Scan(&till, &localDate, &reg, &actor, &lines, &total); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if till != "till-7" || reg != "reg-R" || actor != "u1" || lines != 4 || total != 1999 {
		t.Fatalf("stored = till %q reg %q actor %q lines %d total %d", till, reg, actor, lines, total)
	}
	if want := eod1012Day(t, d, at); localDate != want {
		t.Fatalf("local_date = %q, want %q (InsertClearedBasket's rule)", localDate, want)
	}
	own, err := repo.LocalClearedBasketsSince(ctx, "", "", 50)
	if err != nil || len(own) != 0 {
		t.Fatalf("LocalClearedBasketsSince after a journaled apply = %+v (%v), want none", own, err)
	}
	for name, bad := range map[string]data.ClearedBasket{
		"no id":         {CreatedAt: e.CreatedAt, TillID: "till-7"},
		"no created_at": {ID: "x1", TillID: "till-7"},
		"no till":       {ID: "x2", CreatedAt: e.CreatedAt},
	} {
		if _, err := repo.ApplyJournaledClearedBasket(ctx, bad); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}
