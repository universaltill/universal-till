package data_test

import (
	"context"
	"testing"
	"time"
)

// ut-docs#3034 (follow-up of #2723): UpsertLocalOnly is the one write
// allowed to LOWER primary_synced back to 0 -- Upsert's own primary_synced =
// MAX(held_sales.primary_synced, excluded.primary_synced) deliberately never
// can (other callers depend on that stickiness, see HeldSale.PrimarySynced).
// It replaces the #2723 fix's Upsert-then-MarkLocalOnly pair of statements
// (removed: MarkLocalOnly) with a SINGLE statement -- see the repo's own doc
// comment for why the two-statement version left a window a concurrent
// ReconcileWithPrimary could observe.
func TestHeldSalesRepo_UpsertLocalOnly_InsertStartsUnsynced(t *testing.T) {
	repo, _ := newHeldSalesTombstoneRepo(t)
	ctx := context.Background()

	seed := tombstoneTestHeldSale
	seed.PrimarySynced = true // even a caller passing true must not matter: insert is always 0.
	if err := repo.UpsertLocalOnly(ctx, seed); err != nil {
		t.Fatal(err)
	}
	got, ok, err := repo.Get(ctx, seed.ID)
	if err != nil || !ok || got.PrimarySynced {
		t.Fatalf("an insert via UpsertLocalOnly must start primary_synced=false regardless of h.PrimarySynced, got ok=%v %+v err=%v", ok, got, err)
	}
	if got.CreatedAt == "" {
		t.Fatalf("an insert must still stamp created_at, got %+v", got)
	}
}

// The update branch: a row already confirmed on the primary
// (primary_synced=1, from an earlier successful mirror of the SAME id) must
// be demoted to 0 by UpsertLocalOnly -- the #2723 give-back scenario -- with
// its content updated and created_at left alone (same convention as Upsert).
func TestHeldSalesRepo_UpsertLocalOnly_UpdateDemotesAndUpdatesContent(t *testing.T) {
	repo, _ := newHeldSalesTombstoneRepo(t)
	ctx := context.Background()

	seed := tombstoneTestHeldSale
	seed.PrimarySynced = true
	if err := repo.Upsert(ctx, seed); err != nil {
		t.Fatal(err)
	}
	before, ok, err := repo.Get(ctx, seed.ID)
	if err != nil || !ok || !before.PrimarySynced {
		t.Fatalf("precondition: the seeded row must be a confirmed mirror, got ok=%v %+v err=%v", ok, before, err)
	}

	updated := seed
	updated.Label = "Table 9"
	updated.Payload = `{"lines":[{"sku":"XYZ"}]}`
	updated.TotalMinor = 999
	updated.LineCount = 1
	updated.PrimarySynced = true // even true in: the update branch is hardwired to 0.
	if err := repo.UpsertLocalOnly(ctx, updated); err != nil {
		t.Fatal(err)
	}

	got, ok, err := repo.Get(ctx, seed.ID)
	if err != nil || !ok {
		t.Fatalf("the row must still exist, got ok=%v err=%v", ok, err)
	}
	if got.PrimarySynced {
		t.Fatalf("UpsertLocalOnly must demote a confirmed mirror to primary_synced=false, got %+v", got)
	}
	if got.Label != "Table 9" || got.Payload != updated.Payload || got.TotalMinor != 999 || got.LineCount != 1 {
		t.Fatalf("UpsertLocalOnly must still write through the row's new content, got %+v", got)
	}
	if got.CreatedAt != before.CreatedAt {
		t.Fatalf("UpsertLocalOnly must leave created_at alone on the update branch (same as Upsert), got %q want %q", got.CreatedAt, before.CreatedAt)
	}

	// The card's actual AC: the next successful primary list does not include
	// this id (it was claimed off the primary and never landed back there) --
	// the row must survive as a genuinely local-only order, not be dropped as
	// "resolved elsewhere".
	dropped, err := repo.ReconcileWithPrimary(ctx, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if dropped != 0 {
		t.Fatalf("a row UpsertLocalOnly cleared must not be dropped as a resolved mirror, got dropped=%d", dropped)
	}
	if _, ok, err := repo.Get(ctx, seed.ID); err != nil || !ok {
		t.Fatalf("the row must survive ReconcileWithPrimary after UpsertLocalOnly, got ok=%v err=%v", ok, err)
	}
}

// Upsert itself must still never lower the flag (unchanged behaviour,
// guarded here so a future edit to either method can't silently blur the
// line between them).
func TestHeldSalesRepo_Upsert_StillNeverLowersPrimarySynced(t *testing.T) {
	repo, _ := newHeldSalesTombstoneRepo(t)
	ctx := context.Background()

	seed := tombstoneTestHeldSale
	seed.PrimarySynced = true
	if err := repo.Upsert(ctx, seed); err != nil {
		t.Fatal(err)
	}

	again := seed
	again.PrimarySynced = false
	if err := repo.Upsert(ctx, again); err != nil {
		t.Fatal(err)
	}
	got, ok, err := repo.Get(ctx, seed.ID)
	if err != nil || !ok || !got.PrimarySynced {
		t.Fatalf("Upsert must never lower primary_synced (MAX semantics), got ok=%v %+v err=%v", ok, got, err)
	}
}
