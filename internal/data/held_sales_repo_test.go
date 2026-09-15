package data

import (
	"context"
	"testing"
)

// ADR-0093 (ut-docs#1920): held_sales.updated_at is the cross-till ordering
// key, and UpsertIfNewer is the primary-side guarded write behind POST
// /api/sync/held-sales/upsert. These pin the two properties everything
// above the repo relies on: every content-changing write moves the stamp,
// and the guard refuses an older stamp without erroring.

// heldSaleStampShape is what datetime('now') produces -- the same UTC text
// created_at already uses, so the two compare as plain strings.
func assertHeldSaleStamp(t *testing.T, what, stamp string) {
	t.Helper()
	if len(stamp) != len("2006-01-02 15:04:05") || stamp[4] != '-' || stamp[10] != ' ' {
		t.Fatalf("%s: updated_at must be a datetime('now') stamp, got %q", what, stamp)
	}
}

func TestHeldSalesRepo_UpdatedAtAdvancesOnEveryContentWrite(t *testing.T) {
	repo := newHeldSalesTestDB(t)
	ctx := context.Background()

	// Upsert-as-insert stamps it -- a fresh first park is never the schema
	// default (Insert itself was removed, ut-docs#1920 CI review: it had no
	// production caller left once parkCurrentBasket switched to the
	// write-through, so Upsert is now the only insert-or-update path).
	if err := repo.Upsert(ctx, HeldSale{ID: "h1", Label: "Table 4", Payload: `{}`}); err != nil {
		t.Fatal(err)
	}
	got, _, err := repo.Get(ctx, "h1")
	if err != nil {
		t.Fatal(err)
	}
	assertHeldSaleStamp(t, "after Insert", got.UpdatedAt)
	if list, err := repo.List(ctx); err != nil || len(list) != 1 || list[0].UpdatedAt != got.UpdatedAt {
		t.Fatalf("List must scan updated_at like Get does, got %+v err=%v", list, err)
	}

	// Upsert honours an explicit stamp (the replica-side mirror of a row
	// the PRIMARY already stamped) on both its insert and update paths...
	if err := repo.Upsert(ctx, HeldSale{ID: "h1", Label: "Table 4", Payload: `{}`, UpdatedAt: "2020-01-01 00:00:00"}); err != nil {
		t.Fatal(err)
	}
	if got, _, _ = repo.Get(ctx, "h1"); got.UpdatedAt != "2020-01-01 00:00:00" {
		t.Fatalf("Upsert-update must honour an explicit updated_at, got %q", got.UpdatedAt)
	}
	if err := repo.Upsert(ctx, HeldSale{ID: "h2", Label: "Mirror", Payload: `{}`, UpdatedAt: "2021-02-03 04:05:06"}); err != nil {
		t.Fatal(err)
	}
	if got2, _, _ := repo.Get(ctx, "h2"); got2.UpdatedAt != "2021-02-03 04:05:06" {
		t.Fatalf("Upsert-insert must honour an explicit updated_at, got %q", got2.UpdatedAt)
	}
	// ...and stamps now when none is given (every ordinary re-park).
	if err := repo.Upsert(ctx, HeldSale{ID: "h1", Label: "Table 4", Payload: `{"lines":[{}]}`}); err != nil {
		t.Fatal(err)
	}
	got, _, _ = repo.Get(ctx, "h1")
	assertHeldSaleStamp(t, "after Upsert with no stamp", got.UpdatedAt)
	if got.UpdatedAt <= "2020-01-01 00:00:00" {
		t.Fatalf("Upsert with no stamp must advance updated_at past the old value, got %q", got.UpdatedAt)
	}

	// SetTable is a content change under the ordering key too.
	if err := repo.Upsert(ctx, HeldSale{ID: "h1", Label: "Table 4", Payload: `{}`, UpdatedAt: "2020-01-01 00:00:00"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetTable(ctx, "h1", "tbl-9"); err != nil {
		t.Fatal(err)
	}
	got, _, _ = repo.Get(ctx, "h1")
	if got.TableID != "tbl-9" {
		t.Fatalf("SetTable must still move the table, got %+v", got)
	}
	assertHeldSaleStamp(t, "after SetTable", got.UpdatedAt)
	if got.UpdatedAt <= "2020-01-01 00:00:00" {
		t.Fatalf("SetTable must advance updated_at, got %q", got.UpdatedAt)
	}
}

func TestHeldSalesRepo_UpsertIfNewer_OrderingGuard(t *testing.T) {
	repo := newHeldSalesTestDB(t)
	ctx := context.Background()

	// A row that does not exist yet is always applied, whatever its stamp.
	applied, err := repo.UpsertIfNewer(ctx, HeldSale{ID: "h1", Label: "v1", TotalMinor: 100, Payload: `{}`, CreatedAt: "2026-09-01 10:00:00", UpdatedAt: "2026-09-01 10:00:00"})
	if err != nil || !applied {
		t.Fatalf("first write must be applied, got applied=%v err=%v", applied, err)
	}
	got, ok, err := repo.Get(ctx, "h1")
	if err != nil || !ok || got.Label != "v1" || got.UpdatedAt != "2026-09-01 10:00:00" || got.CreatedAt != "2026-09-01 10:00:00" {
		t.Fatalf("unexpected row after first write: %+v ok=%v err=%v", got, ok, err)
	}

	// An OLDER stamp is refused -- applied=false, no error, row untouched.
	applied, err = repo.UpsertIfNewer(ctx, HeldSale{ID: "h1", Label: "stale", TotalMinor: 1, Payload: `{"stale":true}`, UpdatedAt: "2026-09-01 09:59:59"})
	if err != nil {
		t.Fatalf("a refused write must not be an error, got %v", err)
	}
	if applied {
		t.Fatal("an older stamp must be refused")
	}
	if got, _, _ = repo.Get(ctx, "h1"); got.Label != "v1" || got.TotalMinor != 100 || got.UpdatedAt != "2026-09-01 10:00:00" {
		t.Fatalf("a refused write must leave the row untouched, got %+v", got)
	}

	// An EQUAL stamp is applied: stamps have one-second resolution, and a
	// re-park within the same second as the previous write is still the
	// latest write.
	applied, err = repo.UpsertIfNewer(ctx, HeldSale{ID: "h1", Label: "v2", TotalMinor: 200, Payload: `{}`, UpdatedAt: "2026-09-01 10:00:00"})
	if err != nil || !applied {
		t.Fatalf("an equal stamp must be applied, got applied=%v err=%v", applied, err)
	}
	if got, _, _ = repo.Get(ctx, "h1"); got.Label != "v2" || got.TotalMinor != 200 {
		t.Fatalf("an applied write must refresh the contents, got %+v", got)
	}

	// A NEWER stamp is applied; created_at follows Upsert's rule (untouched
	// on update, even when the caller sends a different one).
	applied, err = repo.UpsertIfNewer(ctx, HeldSale{ID: "h1", Label: "v3", TotalMinor: 300, Payload: `{}`, TableID: "tbl-1", CreatedAt: "2030-01-01 00:00:00", UpdatedAt: "2026-09-01 10:05:00"})
	if err != nil || !applied {
		t.Fatalf("a newer stamp must be applied, got applied=%v err=%v", applied, err)
	}
	got, _, _ = repo.Get(ctx, "h1")
	if got.Label != "v3" || got.TableID != "tbl-1" || got.UpdatedAt != "2026-09-01 10:05:00" {
		t.Fatalf("unexpected row after newer write: %+v", got)
	}
	if got.CreatedAt != "2026-09-01 10:00:00" {
		t.Fatalf("UpsertIfNewer must leave created_at at the first park, got %q", got.CreatedAt)
	}

	// An EMPTY stamp is "now" on this clock -- a replica's own fresh write
	// -- and so beats any stored stamp from the past.
	applied, err = repo.UpsertIfNewer(ctx, HeldSale{ID: "h1", Label: "v4", Payload: `{}`})
	if err != nil || !applied {
		t.Fatalf("an empty stamp (now) must be applied over a past stamp, got applied=%v err=%v", applied, err)
	}
	got, _, _ = repo.Get(ctx, "h1")
	if got.Label != "v4" {
		t.Fatalf("unexpected row after now-stamped write: %+v", got)
	}
	assertHeldSaleStamp(t, "after empty-stamp write", got.UpdatedAt)
	if got.UpdatedAt <= "2026-09-01 10:05:00" {
		t.Fatalf("an empty stamp must be stamped now, got %q", got.UpdatedAt)
	}

	// ...and a stored future stamp (a clock that ran ahead) refuses "now",
	// which is the guard doing its job, not a bug.
	if err := repo.Upsert(ctx, HeldSale{ID: "h1", Label: "future", Payload: `{}`, UpdatedAt: "2999-01-01 00:00:00"}); err != nil {
		t.Fatal(err)
	}
	if applied, err = repo.UpsertIfNewer(ctx, HeldSale{ID: "h1", Label: "v5", Payload: `{}`}); err != nil || applied {
		t.Fatalf("now must lose to a stored future stamp, got applied=%v err=%v", applied, err)
	}

	// Never duplicates a row.
	if list, err := repo.List(ctx); err != nil || len(list) != 1 {
		t.Fatalf("UpsertIfNewer must never duplicate a row, got %d err=%v", len(list), err)
	}
}
