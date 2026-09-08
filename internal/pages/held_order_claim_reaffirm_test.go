package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
)

// TestHeldOrderClaimReaffirmTick_RecoversADeletedClaimWithinOneTick
// (ut-docs#1724): the periodic sibling of Init's boot re-claim step
// (ut-docs#1704). Independent review (2026-09-08) found the first version of
// this test proved the WRONG property: it asserted on tills.last_seen_at
// staying fresh, which is refreshed by ANY authenticated sync call this till
// makes (including the pre-existing 30s admin-pull tick) — not something
// this change contributes at all. What this change actually provides is
// RECOVERY: if the PRIMARY-side table_claims row for a held order's table is
// simply gone (taken over by a different till and since released, wiped by
// some other race, or any other reason nothing else revisits) this till's
// next tick re-creates it — bounded to ~one tick interval instead of
// "whenever this till next restarts" (the only prior trigger). This test
// drives heldOrderClaimReaffirmTick directly (same "drive the tick, not a
// real ticker" pattern syncPullTick's own tests already use) and asserts on
// the one property only this change provides: the deleted row comes back,
// owned by this till.
func TestHeldOrderClaimReaffirmTick_RecoversADeletedClaimWithinOneTick(t *testing.T) {
	chdirRoot(t)

	dbase, err := db.Open(filepath.Join(t.TempDir(), "primary.db"))
	if err != nil {
		t.Fatalf("open primary db: %v", err)
	}
	defer dbase.Close()
	primaryDp := &common.Deps{Db: dbase.DB}
	mux := http.NewServeMux()
	registerSyncTablesClaim(mux, primaryDp)
	primary := httptest.NewServer(mux)
	defer primary.Close()

	primaryRepo := data.NewPOSRepo(dbase.DB)
	ctx := context.Background()
	tableID, err := primaryRepo.CreateTable(ctx, "T1", "", 4, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	if _, err := data.NewTillsRepo(dbase.DB).InsertTill(ctx, "Replica", hashBearer("b-123")); err != nil {
		t.Fatalf("seed till: %v", err)
	}
	// No table_claims row seeded at all -- simulating exactly the state a
	// takeover-then-release (or any other race that drops the row) leaves
	// behind: the held order is still parked, but nothing on the primary
	// says so any more, so every OTHER till reads this table as free.
	if free, err := primaryRepo.IsTableFree(ctx, tableID, ""); err != nil || !free {
		t.Fatalf("precondition: T1 must read free with no claim row, got free=%v err=%v", free, err)
	}

	d, err := db.Open(filepath.Join(t.TempDir(), "replica.db"))
	if err != nil {
		t.Fatalf("open replica db: %v", err)
	}
	defer d.Close()
	store := settings.NewStore(d.DB)
	setReplicaSettings(t, store, primary.URL, "b-123")
	if _, err := d.DB.Exec(`INSERT INTO tables (id, label, area_zone, seat_count, shape, pos_x, pos_y, enabled, created_at, updated_at) VALUES (?,?,?,?,?,?,?,1,datetime('now'),datetime('now'))`,
		tableID, "T1", "", 4, "rect", 100, 100); err != nil {
		t.Fatalf("mirror table onto replica: %v", err)
	}
	if _, err := d.DB.Exec(`INSERT INTO held_sales (id, label, total_minor, line_count, payload, table_id) VALUES ('h1','Table 1',100,1,'{}',?)`, tableID); err != nil {
		t.Fatalf("seed held sale: %v", err)
	}
	replicaDp := &common.Deps{Db: d.DB, Settings: store}
	replicaRepo := data.NewPOSRepo(d.DB)

	heldOrderClaimReaffirmTick(ctx, replicaDp, replicaRepo)

	// The primary must now show T1 occupied again -- the tick re-created the
	// missing claim row for this till's held order, without a restart.
	if free, err := primaryRepo.IsTableFree(ctx, tableID, ""); err != nil || free {
		t.Fatalf("after periodic re-affirm, T1 must read occupied on the primary again (the missing claim must be re-created) -- free=%v err=%v", free, err)
	}
}

// TestHeldOrderClaimReaffirmTick_CannotEvictALiveTillsLegitimateClaim
// (ut-docs#1724, independent review 2026-09-08): the boundary this change
// must respect. If a DIFFERENT, currently live till has legitimately taken
// this table over (its own tills.last_seen_at is fresh), the tick's
// write-through attempt is refused, exactly like any other ordinary "someone
// else has it" claim outcome -- it must never forcibly evict a live till's
// claim just because a held order elsewhere still thinks it owns that table.
func TestHeldOrderClaimReaffirmTick_CannotEvictALiveTillsLegitimateClaim(t *testing.T) {
	chdirRoot(t)

	dbase, err := db.Open(filepath.Join(t.TempDir(), "primary.db"))
	if err != nil {
		t.Fatalf("open primary db: %v", err)
	}
	defer dbase.Close()
	primaryDp := &common.Deps{Db: dbase.DB}
	mux := http.NewServeMux()
	registerSyncTablesClaim(mux, primaryDp)
	primary := httptest.NewServer(mux)
	defer primary.Close()

	primaryRepo := data.NewPOSRepo(dbase.DB)
	ctx := context.Background()
	tableID, err := primaryRepo.CreateTable(ctx, "T1", "", 4, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	if _, err := data.NewTillsRepo(dbase.DB).InsertTill(ctx, "Replica", hashBearer("b-123")); err != nil {
		t.Fatalf("seed till: %v", err)
	}
	otherTillID, err := data.NewTillsRepo(dbase.DB).InsertTill(ctx, "Other", hashBearer("b-456"))
	if err != nil {
		t.Fatalf("seed other till: %v", err)
	}
	// InsertTill leaves last_seen_at NULL until a real request touches it
	// (syncTill) -- a NULL last_seen_at reads as stale, not live, so it must
	// be set explicitly here to actually simulate "a different till is
	// currently online and holding this table", the scenario this test
	// exists to check.
	if _, err := dbase.DB.ExecContext(ctx, `UPDATE tills SET last_seen_at = ? WHERE id = ?`, time.Now().UTC().Format(time.RFC3339), otherTillID); err != nil {
		t.Fatalf("mark other till as currently seen: %v", err)
	}
	if claimed, err := primaryRepo.ClaimTableForTill(ctx, tableID, otherTillID, time.Now().Add(-tillClaimTTL)); err != nil || !claimed {
		t.Fatalf("seed the other till's live claim: claimed=%v err=%v", claimed, err)
	}

	d, err := db.Open(filepath.Join(t.TempDir(), "replica.db"))
	if err != nil {
		t.Fatalf("open replica db: %v", err)
	}
	defer d.Close()
	store := settings.NewStore(d.DB)
	setReplicaSettings(t, store, primary.URL, "b-123")
	if _, err := d.DB.Exec(`INSERT INTO tables (id, label, area_zone, seat_count, shape, pos_x, pos_y, enabled, created_at, updated_at) VALUES (?,?,?,?,?,?,?,1,datetime('now'),datetime('now'))`,
		tableID, "T1", "", 4, "rect", 100, 100); err != nil {
		t.Fatalf("mirror table onto replica: %v", err)
	}
	if _, err := d.DB.Exec(`INSERT INTO held_sales (id, label, total_minor, line_count, payload, table_id) VALUES ('h1','Table 1',100,1,'{}',?)`, tableID); err != nil {
		t.Fatalf("seed held sale: %v", err)
	}
	replicaDp := &common.Deps{Db: d.DB, Settings: store}
	replicaRepo := data.NewPOSRepo(d.DB)

	heldOrderClaimReaffirmTick(ctx, replicaDp, replicaRepo) // must not panic, must not evict the other till

	var claimedBy string
	if err := dbase.DB.QueryRowContext(ctx, `SELECT till_id FROM table_claims WHERE table_id = ?`, tableID).Scan(&claimedBy); err != nil {
		t.Fatalf("read table_claims after re-affirm attempt: %v", err)
	}
	if claimedBy != otherTillID {
		t.Fatalf("the other, live till's claim must survive the re-affirm attempt untouched -- till_id=%q want=%q", claimedBy, otherTillID)
	}
}

// TestHeldOrderClaimReaffirmTick_NoHeldOrdersIsANoOp (ut-docs#1724): the
// common case -- a till with nothing parked -- must not attempt any
// write-through call. Regression against re-introducing a wasted network
// round trip on every tick for the overwhelmingly common empty case.
func TestHeldOrderClaimReaffirmTick_NoHeldOrdersIsANoOp(t *testing.T) {
	chdirRoot(t)

	// No primary configured at all -- if the tick tried to write-through
	// anything, claimTableWriteThrough's own primary call would need
	// sync.primary_url/sync.bearer and there would be nothing to call
	// through to. Settings left nil entirely: a call into it would panic,
	// which is exactly the regression this guards against.
	d, err := db.Open(filepath.Join(t.TempDir(), "replica.db"))
	if err != nil {
		t.Fatalf("open replica db: %v", err)
	}
	defer d.Close()
	dp := &common.Deps{Db: d.DB}
	posRepo := data.NewPOSRepo(d.DB)

	heldOrderClaimReaffirmTick(context.Background(), dp, posRepo) // must not panic, must not touch Settings
}
