package data_test

import (
	"context"
	"testing"
)

// ut-docs#2723: MarkLocalOnly is the one path allowed to LOWER
// primary_synced back to 0 -- Upsert's own primary_synced =
// MAX(held_sales.primary_synced, excluded.primary_synced) deliberately never
// can (other callers depend on that stickiness, see HeldSale.PrimarySynced).
// heldSaleGiveBack (held_sale_sync_proxy.go) calls this when its own
// write-through's primary upsert fails: without it, a row this till had
// already confirmed on the primary (primary_synced=1, from an earlier
// successful mirror of the SAME id) stays stuck at 1 forever, even though
// the primary no longer holds it -- and the next successful
// ReconcileWithPrimary then drops it as "resolved elsewhere", losing the
// only surviving copy of a genuinely open order.
func TestHeldSalesRepo_MarkLocalOnly_SurvivesReconcileWithPrimary(t *testing.T) {
	repo, _ := newHeldSalesTombstoneRepo(t)
	ctx := context.Background()

	// A row already confirmed on the primary (an earlier successful mirror).
	seed := tombstoneTestHeldSale
	seed.PrimarySynced = true
	if err := repo.Upsert(ctx, seed); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := repo.Get(ctx, seed.ID); err != nil || !ok || !got.PrimarySynced {
		t.Fatalf("precondition: the seeded row must be a confirmed mirror, got ok=%v %+v err=%v", ok, got, err)
	}

	if err := repo.MarkLocalOnly(ctx, seed.ID); err != nil {
		t.Fatal(err)
	}
	got, ok, err := repo.Get(ctx, seed.ID)
	if err != nil || !ok || got.PrimarySynced {
		t.Fatalf("MarkLocalOnly must clear primary_synced to false, got ok=%v %+v err=%v", ok, got, err)
	}

	// The next successful primary list does not include this id (it was
	// claimed off the primary and never landed back there) -- exactly the
	// held-sale give-back scenario. Without MarkLocalOnly, the row would
	// still read primary_synced=1 and be dropped here as "resolved
	// elsewhere"; with it cleared, ReconcileWithPrimary must leave it alone.
	dropped, err := repo.ReconcileWithPrimary(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if dropped != 0 {
		t.Fatalf("a row MarkLocalOnly cleared must not be dropped as a resolved mirror, got dropped=%d", dropped)
	}
	if _, ok, err := repo.Get(ctx, seed.ID); err != nil || !ok {
		t.Fatalf("the row must survive ReconcileWithPrimary after MarkLocalOnly, got ok=%v err=%v", ok, err)
	}
}
