package data

import (
	"context"
	"testing"
	"time"
)

// ut-docs#3038: ReconcileWithPrimary's presentIDs come from a list fetched
// BEFORE its transaction. A resume on this till can claim an order off the
// primary and then give it back local-only (UpsertLocalOnly, primary_synced
// = 0) while that fetch is still in flight. The stale list still names the
// order, and without the fetch-start guard the reconcile raised it back to
// primary_synced = 1 -- so the next successful reconcile (the primary no
// longer lists it) dropped the only copy of an open order.
func TestHeldSalesRepo_ReconcileWithPrimary_StaleListDoesNotConfirmLaterLocalOnlyWrite(t *testing.T) {
	repo := newHeldSalesTestDB(t)
	ctx := context.Background()
	const id = "hold-stale-list"

	// A confirmed mirror from an earlier write-through.
	if err := repo.Upsert(ctx, HeldSale{ID: id, Label: "Table 4", Payload: `{}`, PrimarySynced: true}); err != nil {
		t.Fatal(err)
	}

	// 1. An Open orders render starts fetching the primary's list; the
	//    primary still holds the order, so the list will include it.
	fetchStartedAt := time.Now()
	// 2–3. Meanwhile a resume on this till claims the order, fails, and its
	//      give-back falls back to local-only.
	if err := repo.UpsertLocalOnly(ctx, HeldSale{ID: id, Label: "Table 4", Payload: `{}`}); err != nil {
		t.Fatal(err)
	}
	// 4. The stale fetch's reconcile commits.
	if _, err := repo.ReconcileWithPrimary(ctx, []string{id}, fetchStartedAt); err != nil {
		t.Fatal(err)
	}
	got, ok, err := repo.Get(ctx, id)
	if err != nil || !ok {
		t.Fatalf("the order must still exist, ok=%v err=%v", ok, err)
	}
	if got.PrimarySynced {
		t.Fatalf("a list fetched before the local-only give-back must not confirm it (primary_synced must stay 0), got %+v", got)
	}

	// 5. The next successful reconcile no longer sees the order on the
	//    primary (it was claimed off it): the local-only copy must survive.
	dropped, err := repo.ReconcileWithPrimary(ctx, nil, time.Now().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if dropped != 0 {
		t.Fatalf("the open order was dropped as a resolved mirror (dropped=%d)", dropped)
	}
	if _, ok, err := repo.Get(ctx, id); err != nil || !ok {
		t.Fatalf("the open order was lost, ok=%v err=%v", ok, err)
	}
}

// The guard only holds back rows written AFTER the fetch started: a
// never-confirmed row that predates the fetch is still confirmed by a list
// naming it, and an already-confirmed row is never touched by the raise,
// whatever its updated_at (a mirror carries the primary's clock, which may
// run ahead of this till's).
func TestHeldSalesRepo_ReconcileWithPrimary_GuardOnlyHoldsBackLaterWrites(t *testing.T) {
	repo := newHeldSalesTestDB(t)
	ctx := context.Background()
	if err := repo.Upsert(ctx, HeldSale{ID: "older-local", Label: "a", Payload: `{}`}); err != nil {
		t.Fatal(err)
	}
	if applied, err := repo.UpsertIfNewer(ctx, HeldSale{ID: "future-mirror", Label: "b", Payload: `{}`, UpdatedAt: "2999-01-01 00:00:00", PrimarySynced: true}); err != nil || !applied {
		t.Fatalf("seed future mirror: applied=%v err=%v", applied, err)
	}
	fetchStartedAt := time.Now().Add(time.Second)
	if _, err := repo.ReconcileWithPrimary(ctx, []string{"older-local", "future-mirror"}, fetchStartedAt); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"older-local", "future-mirror"} {
		got, ok, err := repo.Get(ctx, id)
		if err != nil || !ok || !got.PrimarySynced {
			t.Fatalf("%s: must be kept and confirmed, got ok=%v %+v err=%v", id, ok, got, err)
		}
	}
}
