package data

// Table floor plan (universaltill/ut-docs#814, ADR-0054): per-store dining
// tables with a name, area/zone, seat count, shape and a persisted position
// on a fixed 1000×1000 logical canvas. Tables are soft-disabled (enabled=0),
// never deleted — order history may reference them once ut-docs#820 wires
// table assignment onto held sales.
//
// ListTablesWithState is the live free/occupied query. Until #820 ships,
// nothing writes held_sales.table_id, so every table legitimately reads as
// free — that is asserted here as a real case, not a placeholder. The
// occupied branch is exercised by seeding a held_sales row with table_id set
// directly (raw SQL is fine in tests), proving the join works the moment
// #820 starts writing the column.

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/db"
)

func openTablesTestDB(t *testing.T) (*db.DB, *POSRepo) {
	t.Helper()
	dbo, err := db.Open(filepath.Join(t.TempDir(), "tables.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { dbo.Close() })
	return dbo, NewPOSRepo(dbo.DB)
}

func TestTableCRUDAndReload(t *testing.T) {
	_, repo := openTablesTestDB(t)
	ctx := context.Background()

	id, err := repo.CreateTable(ctx, "T1", "Terrace", 4, "rect", 200, 300)
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	if id == "" {
		t.Fatal("CreateTable returned empty id")
	}

	list, err := repo.ListTables(ctx)
	if err != nil {
		t.Fatalf("ListTables: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("want 1 table, got %d", len(list))
	}
	tb := list[0]
	if tb.ID != id || tb.Label != "T1" || tb.AreaZone != "Terrace" || tb.SeatCount != 4 ||
		tb.Shape != "rect" || tb.PosX != 200 || tb.PosY != 300 {
		t.Fatalf("unexpected table: %+v", tb)
	}
	if !tb.Enabled {
		t.Fatal("new table must be enabled")
	}
	if tb.CreatedAt == "" || tb.UpdatedAt == "" {
		t.Fatalf("timestamps must be set: %+v", tb)
	}

	// Edit any time (not just at setup), then reload unchanged.
	if err := repo.UpdateTable(ctx, id, "Table 1", "Main room", 6, "round"); err != nil {
		t.Fatalf("UpdateTable: %v", err)
	}
	list, err = repo.ListTables(ctx)
	if err != nil {
		t.Fatalf("ListTables after update: %v", err)
	}
	tb = list[0]
	if tb.Label != "Table 1" || tb.AreaZone != "Main room" || tb.SeatCount != 6 || tb.Shape != "round" {
		t.Fatalf("update not persisted: %+v", tb)
	}
	// The update must not disturb the saved position.
	if tb.PosX != 200 || tb.PosY != 300 {
		t.Fatalf("update moved the table: %+v", tb)
	}
}

func TestTablePositionPersistsAndClamps(t *testing.T) {
	_, repo := openTablesTestDB(t)
	ctx := context.Background()

	id, err := repo.CreateTable(ctx, "T1", "", 2, "round", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	if err := repo.SetTablePosition(ctx, id, 640, 480); err != nil {
		t.Fatalf("SetTablePosition: %v", err)
	}
	list, _ := repo.ListTables(ctx)
	if list[0].PosX != 640 || list[0].PosY != 480 {
		t.Fatalf("position not persisted: %+v", list[0])
	}

	// Positions are logical-canvas units (0..1000) — out-of-range writes are
	// clamped in the one place that owns the write, not trusted from the
	// client. The clamp keeps an edge inset (data.TableEdgeInset) so the
	// table's own shape never renders clipped against the canvas edge
	// (2026-08-19 code review, ut-docs#814).
	if err := repo.SetTablePosition(ctx, id, -50, 4000); err != nil {
		t.Fatalf("SetTablePosition out of range: %v", err)
	}
	list, _ = repo.ListTables(ctx)
	if list[0].PosX != TableEdgeInset || list[0].PosY != TableCanvasSize-TableEdgeInset {
		t.Fatalf("position not clamped to canvas: %+v", list[0])
	}

	if err := repo.SetTablePosition(ctx, "nope", 1, 1); err == nil {
		t.Fatal("SetTablePosition on missing id must error")
	}
}

func TestTableInvalidShapeRejected(t *testing.T) {
	_, repo := openTablesTestDB(t)
	ctx := context.Background()

	if _, err := repo.CreateTable(ctx, "T1", "", 2, "triangle", 0, 0); err == nil {
		t.Fatal("CreateTable with invalid shape must error")
	}
	id, err := repo.CreateTable(ctx, "T1", "", 2, "rect", 0, 0)
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	if err := repo.UpdateTable(ctx, id, "T1", "", 2, "blob"); err == nil {
		t.Fatal("UpdateTable with invalid shape must error")
	}
}

func TestTableEnableDisable(t *testing.T) {
	_, repo := openTablesTestDB(t)
	ctx := context.Background()

	id, err := repo.CreateTable(ctx, "T1", "", 2, "rect", 0, 0)
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	if err := repo.SetTableEnabled(ctx, id, false); err != nil {
		t.Fatalf("SetTableEnabled: %v", err)
	}
	list, _ := repo.ListTables(ctx)
	if list[0].Enabled {
		t.Fatal("table should be disabled")
	}
	// Soft-disable, never delete: the row is still listed for the admin page.
	if len(list) != 1 {
		t.Fatalf("disabled table must still list: got %d rows", len(list))
	}
	if err := repo.SetTableEnabled(ctx, id, true); err != nil {
		t.Fatalf("re-enable: %v", err)
	}
	list, _ = repo.ListTables(ctx)
	if !list[0].Enabled {
		t.Fatal("table should be re-enabled")
	}

	if err := repo.SetTableEnabled(ctx, "nope", true); err == nil {
		t.Fatal("SetTableEnabled on missing id must error")
	}
	if err := repo.UpdateTable(ctx, "nope", "X", "", 1, "rect"); err == nil {
		t.Fatal("UpdateTable on missing id must error")
	}
}

// Today NOTHING writes held_sales.table_id (that is ut-docs#820), so every
// table must read as free — this is the correct, honest live state until
// #820 ships, and it is a real assertion, not a placeholder.
func TestListTablesWithState_AllFreeBeforeOrderAssignmentShips(t *testing.T) {
	dbo, repo := openTablesTestDB(t)
	ctx := context.Background()

	if _, err := repo.CreateTable(ctx, "T1", "Terrace", 4, "rect", 100, 100); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	if _, err := repo.CreateTable(ctx, "T2", "Terrace", 2, "round", 300, 100); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	// A held sale with NO table assignment (today's only possible shape) must
	// not mark anything occupied.
	if _, err := dbo.DB.Exec(
		`INSERT INTO held_sales (id, label, total_minor, line_count, payload) VALUES ('h1','',0,0,'{}')`); err != nil {
		t.Fatalf("seed held sale: %v", err)
	}

	rows, err := repo.ListTablesWithState(ctx)
	if err != nil {
		t.Fatalf("ListTablesWithState: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 tables, got %d", len(rows))
	}
	for _, r := range rows {
		if r.Occupied || r.OccupiedSince != "" {
			t.Fatalf("table %s must be free before #820 ships: %+v", r.Label, r)
		}
	}
}

// The occupied branch of the same query: a held sale carrying table_id (the
// column #820 will start writing) flips exactly that table to occupied, with
// the OLDEST open order's created_at as the occupied-since timestamp.
func TestListTablesWithState_OccupiedViaHeldSaleTableID(t *testing.T) {
	dbo, repo := openTablesTestDB(t)
	ctx := context.Background()

	busy, err := repo.CreateTable(ctx, "T1", "", 4, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	if _, err := repo.CreateTable(ctx, "T2", "", 2, "round", 300, 100); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	if _, err := dbo.DB.Exec(`
INSERT INTO held_sales (id, label, total_minor, line_count, payload, table_id, created_at) VALUES
 ('h1','',0,0,'{}',?, '2026-08-18 10:30:00'),
 ('h2','',0,0,'{}',?, '2026-08-18 10:05:00')`, busy, busy); err != nil {
		t.Fatalf("seed held sales: %v", err)
	}

	rows, err := repo.ListTablesWithState(ctx)
	if err != nil {
		t.Fatalf("ListTablesWithState: %v", err)
	}
	byLabel := map[string]TableWithState{}
	for _, r := range rows {
		byLabel[r.Label] = r
	}
	t1 := byLabel["T1"]
	if !t1.Occupied || t1.OccupiedSince != "2026-08-18 10:05:00" {
		t.Fatalf("T1 must be occupied since the oldest open order: %+v", t1)
	}
	if t2 := byLabel["T2"]; t2.Occupied || t2.OccupiedSince != "" {
		t.Fatalf("T2 must stay free: %+v", t2)
	}
}

// ut-docs#820: GetTable is the label-resolution lookup a table-assignment
// handler uses so it can hand pos.Service.SetTable a resolved label without
// re-deriving it from a full ListTables call.
func TestGetTable(t *testing.T) {
	_, repo := openTablesTestDB(t)
	ctx := context.Background()

	if _, ok, err := repo.GetTable(ctx, "does-not-exist"); err != nil || ok {
		t.Fatalf("expected no table, got ok=%v err=%v", ok, err)
	}

	id, err := repo.CreateTable(ctx, "T3", "Terrace", 2, "round", 400, 400)
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	got, ok, err := repo.GetTable(ctx, id)
	if err != nil || !ok {
		t.Fatalf("GetTable: ok=%v err=%v", ok, err)
	}
	if got.ID != id || got.Label != "T3" || got.AreaZone != "Terrace" || got.SeatCount != 2 {
		t.Fatalf("unexpected table: %+v", got)
	}
}

// ut-docs#820: IsTableFree backs the "move a held order to a different
// table" validation -- it must refuse to move onto a table another held
// sale already occupies, but must NOT self-block moving a held sale back
// onto (or off of) its own current table.
func TestIsTableFree(t *testing.T) {
	dbo, repo := openTablesTestDB(t)
	ctx := context.Background()

	free, err := repo.CreateTable(ctx, "T1", "", 4, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	busy, err := repo.CreateTable(ctx, "T2", "", 4, "rect", 300, 100)
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	if _, err := dbo.DB.Exec(`
INSERT INTO held_sales (id, label, total_minor, line_count, payload, table_id) VALUES ('h1','',0,0,'{}',?)`, busy); err != nil {
		t.Fatalf("seed held sale: %v", err)
	}

	if ok, err := repo.IsTableFree(ctx, free, ""); err != nil || !ok {
		t.Fatalf("expected T1 free, got ok=%v err=%v", ok, err)
	}
	if ok, err := repo.IsTableFree(ctx, busy, ""); err != nil || ok {
		t.Fatalf("expected T2 occupied, got ok=%v err=%v", ok, err)
	}
	// h1 itself, moving off/back onto its own current table, must not be
	// blocked by its own occupancy.
	if ok, err := repo.IsTableFree(ctx, busy, "h1"); err != nil || !ok {
		t.Fatalf("expected T2 free when excluding its own occupant h1, got ok=%v err=%v", ok, err)
	}
}

// ut-docs#1390: a LIVE (not-yet-held) basket's table pick is persisted as a
// table_claims row the moment it's made, so a second basket can't pick the
// same table. ClaimTable is the race-free primitive: INSERT OR IGNORE on the
// PRIMARY KEY, reporting whether THIS call took the claim.
func TestClaimTable_TakesFreeTableRefusesClaimedOne(t *testing.T) {
	_, repo := openTablesTestDB(t)
	ctx := context.Background()

	id, err := repo.CreateTable(ctx, "T1", "", 4, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	claimed, err := repo.ClaimTable(ctx, id)
	if err != nil || !claimed {
		t.Fatalf("first ClaimTable on a free table: claimed=%v err=%v", claimed, err)
	}
	// The same table again (a second basket): the PK conflict is reported
	// as "not claimed", never as an error and never as a silent success.
	claimed, err = repo.ClaimTable(ctx, id)
	if err != nil {
		t.Fatalf("second ClaimTable must not error: %v", err)
	}
	if claimed {
		t.Fatal("second ClaimTable on an already-claimed table must report claimed=false")
	}
}

// ReleaseTableClaim frees a claimed table, and is a safe no-op on a table
// nobody claimed (same convention as HeldSalesRepo.Delete on a missing row).
func TestReleaseTableClaim_FreesAndIsNoOpWhenUnclaimed(t *testing.T) {
	_, repo := openTablesTestDB(t)
	ctx := context.Background()

	id, err := repo.CreateTable(ctx, "T1", "", 4, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	if err := repo.ReleaseTableClaim(ctx, id); err != nil {
		t.Fatalf("ReleaseTableClaim on an unclaimed table must be a no-op, got: %v", err)
	}
	if claimed, err := repo.ClaimTable(ctx, id); err != nil || !claimed {
		t.Fatalf("ClaimTable: claimed=%v err=%v", claimed, err)
	}
	if err := repo.ReleaseTableClaim(ctx, id); err != nil {
		t.Fatalf("ReleaseTableClaim: %v", err)
	}
	if claimed, err := repo.ClaimTable(ctx, id); err != nil || !claimed {
		t.Fatalf("table must be claimable again after release: claimed=%v err=%v", claimed, err)
	}
}

// ReleaseTableClaim must be scoped to THIS till's own local row (till_id =
// "") and never touch a claim owned by a different till (independent
// review finding on ut-docs#1393). Its one caller,
// releaseTableClaimWriteThrough, always means "release MY OWN local
// claim" — before ForceReleaseTableClaim existed that was equivalent to
// "delete whatever row this table_id has" because the PRIMARY KEY on
// table_id made at most one row possible and you could only ever be
// releasing your own. ForceReleaseTableClaim breaks that assumption on
// purpose (a manager can now clear a claim regardless of ownership), so
// without this scoping a real, reachable sequence reopens the exact
// cross-till double-claim ut-docs#1703 closed: a manager frees a table
// whose OWNING till's basket still thinks it holds it (the till is never
// told), a different till then legitimately claims the same table, and
// the first till's own eventual release (via ReleaseTableClaim, unscoped)
// would delete the SECOND till's live claim instead of its own
// already-gone row.
func TestReleaseTableClaim_NeverTouchesAnotherTillsClaim(t *testing.T) {
	dbo, repo := openTablesTestDB(t)
	ctx := context.Background()

	id, err := repo.CreateTable(ctx, "T1", "", 4, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	if _, err := dbo.DB.Exec(
		`INSERT INTO table_claims (table_id, claimed_at, till_id) VALUES (?, ?, 'other-till')`,
		id, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("seed foreign claim: %v", err)
	}
	if err := repo.ReleaseTableClaim(ctx, id); err != nil {
		t.Fatalf("ReleaseTableClaim: %v", err)
	}
	if ok, err := repo.IsTableFree(ctx, id, ""); err != nil || ok {
		t.Fatalf("a different till's claim must survive a local ReleaseTableClaim call, got ok=%v err=%v", ok, err)
	}
}

// ForceReleaseTableClaim is the manager-initiated "Free table" action's
// primitive (ut-docs#1393): unlike every existing caller of the plain
// Release*/ClaimTableForTill family, this one is invoked completely out of
// band from the automatic claim/release lifecycle, so it must clear a claim
// regardless of which till (if any) holds it, and must never touch a
// genuine held_sales row — a manager freeing a stuck claim must never look
// like silently deleting a real parked order.
func TestForceReleaseTableClaim(t *testing.T) {
	dbo, repo := openTablesTestDB(t)
	ctx := context.Background()

	id, err := repo.CreateTable(ctx, "T1", "", 4, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}

	// No claim, no held sale: idempotent no-op, nothing to report freed.
	released, stillHeld, err := repo.ForceReleaseTableClaim(ctx, id)
	if err != nil || released || stillHeld {
		t.Fatalf("on a free table: released=%v stillHeld=%v err=%v, want false/false/nil", released, stillHeld, err)
	}

	// A claim owned by a DIFFERENT till (the crash-orphaned/never-revisited
	// case this action exists for) is force-released regardless of
	// ownership — ReleaseTableClaimForTill, by contrast, is scoped and
	// would leave this exact row untouched.
	if _, err := dbo.DB.Exec(
		`INSERT INTO table_claims (table_id, claimed_at, till_id) VALUES (?, ?, 'some-other-till')`,
		id, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("seed foreign claim: %v", err)
	}
	if ok, err := repo.IsTableFree(ctx, id, ""); err != nil || ok {
		t.Fatalf("expected T1 occupied by the seeded claim, got ok=%v err=%v", ok, err)
	}
	released, stillHeld, err = repo.ForceReleaseTableClaim(ctx, id)
	if err != nil || !released || stillHeld {
		t.Fatalf("releasing a foreign claim: released=%v stillHeld=%v err=%v, want true/false/nil", released, stillHeld, err)
	}
	if ok, err := repo.IsTableFree(ctx, id, ""); err != nil || !ok {
		t.Fatalf("expected T1 free after force-release, got ok=%v err=%v", ok, err)
	}

	// Idempotent: calling it again on the now-free table is a safe no-op.
	if released, _, err := repo.ForceReleaseTableClaim(ctx, id); err != nil || released {
		t.Fatalf("second call: released=%v err=%v, want false/nil", released, err)
	}

	// A genuine held_sales row is NEVER touched: releasing the claim on a
	// table that ALSO has a real held order must report stillHeld=true and
	// must leave the held order (and hence the table's occupied state)
	// completely alone.
	if claimed, err := repo.ClaimTable(ctx, id); err != nil || !claimed {
		t.Fatalf("ClaimTable: claimed=%v err=%v", claimed, err)
	}
	if _, err := dbo.DB.Exec(
		`INSERT INTO held_sales (id, label, total_minor, line_count, payload, table_id) VALUES ('h1','',0,0,'{}',?)`,
		id); err != nil {
		t.Fatalf("seed held sale: %v", err)
	}
	released, stillHeld, err = repo.ForceReleaseTableClaim(ctx, id)
	if err != nil || !released || !stillHeld {
		t.Fatalf("with a held order attached: released=%v stillHeld=%v err=%v, want true/true/nil", released, stillHeld, err)
	}
	if ok, err := repo.IsTableFree(ctx, id, ""); err != nil || ok {
		t.Fatalf("a real held order must still occupy the table after force-release, got ok=%v err=%v", ok, err)
	}
}

// The regression for the reported bug (ut-docs#1390): a table with ONLY a
// live claim (no held_sales row) must read as occupied — before this,
// IsTableFree only looked at held_sales, so the live basket's pick reserved
// nothing.
func TestIsTableFree_LiveClaimOccupiesTable(t *testing.T) {
	_, repo := openTablesTestDB(t)
	ctx := context.Background()

	id, err := repo.CreateTable(ctx, "T1", "", 4, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	if ok, err := repo.IsTableFree(ctx, id, ""); err != nil || !ok {
		t.Fatalf("expected T1 free before any claim, got ok=%v err=%v", ok, err)
	}
	if claimed, err := repo.ClaimTable(ctx, id); err != nil || !claimed {
		t.Fatalf("ClaimTable: claimed=%v err=%v", claimed, err)
	}
	if ok, err := repo.IsTableFree(ctx, id, ""); err != nil || ok {
		t.Fatalf("expected T1 occupied by its live claim, got ok=%v err=%v", ok, err)
	}
	// excludeHeldSaleID excludes a held_sales row only — never a live claim.
	if ok, err := repo.IsTableFree(ctx, id, "some-held-sale"); err != nil || ok {
		t.Fatalf("a held-sale exclusion must not exclude a live claim, got ok=%v err=%v", ok, err)
	}
	if err := repo.ReleaseTableClaim(ctx, id); err != nil {
		t.Fatalf("ReleaseTableClaim: %v", err)
	}
	if ok, err := repo.IsTableFree(ctx, id, ""); err != nil || !ok {
		t.Fatalf("expected T1 free again after release, got ok=%v err=%v", ok, err)
	}
}

// ClearLocalTableClaims is the boot-time recovery for a claim orphaned by an
// unclean shutdown (independent review finding, ut-docs#1390): pos.Service
// always starts empty, so any table_claims row still present at Init belongs
// to a process that never released it. Without this sweep, that table would
// stay unbookable forever — nothing else ever revisits an orphaned row.
func TestClearLocalTableClaims_WipesExistingRowsAndIsSafeOnEmpty(t *testing.T) {
	_, repo := openTablesTestDB(t)
	ctx := context.Background()

	// Safe no-op with nothing to clear (e.g. a clean-shutdown boot).
	if err := repo.ClearLocalTableClaims(ctx); err != nil {
		t.Fatalf("ClearLocalTableClaims on empty table: %v", err)
	}

	t1, err := repo.CreateTable(ctx, "T1", "", 4, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable T1: %v", err)
	}
	t2, err := repo.CreateTable(ctx, "T2", "", 4, "rect", 200, 200)
	if err != nil {
		t.Fatalf("CreateTable T2: %v", err)
	}
	if claimed, err := repo.ClaimTable(ctx, t1); err != nil || !claimed {
		t.Fatalf("ClaimTable T1: claimed=%v err=%v", claimed, err)
	}
	if claimed, err := repo.ClaimTable(ctx, t2); err != nil || !claimed {
		t.Fatalf("ClaimTable T2: claimed=%v err=%v", claimed, err)
	}

	if err := repo.ClearLocalTableClaims(ctx); err != nil {
		t.Fatalf("ClearLocalTableClaims: %v", err)
	}

	if ok, err := repo.IsTableFree(ctx, t1, ""); err != nil || !ok {
		t.Errorf("T1 must be free after ClearLocalTableClaims, got ok=%v err=%v", ok, err)
	}
	if ok, err := repo.IsTableFree(ctx, t2, ""); err != nil || !ok {
		t.Errorf("T2 must be free after ClearLocalTableClaims, got ok=%v err=%v", ok, err)
	}
}

// ...and it must leave a REPLICA's claim alone (ut-docs#1703, independent
// review 2026-09-07). On a primary, table_claims also holds the live claims
// of tills that are still running; the old unscoped `DELETE FROM
// table_claims` wiped those on every primary restart, so the replica kept its
// table on screen while the primary read it free and handed it to the next
// till that asked — the exact cross-till double-claim #1703 closes, re-opened
// by a reboot. Replica rows are TTL-reconciled in ClaimTableForTill instead.
func TestClearLocalTableClaims_LeavesAnotherTillsClaimAlone(t *testing.T) {
	dbo, repo := openTablesTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	seedTill(t, dbo, "till-a", now.Format(time.RFC3339))

	own, err := repo.CreateTable(ctx, "T1", "", 4, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable T1: %v", err)
	}
	replica, err := repo.CreateTable(ctx, "T2", "", 4, "rect", 200, 200)
	if err != nil {
		t.Fatalf("CreateTable T2: %v", err)
	}
	if claimed, err := repo.ClaimTable(ctx, own); err != nil || !claimed {
		t.Fatalf("local ClaimTable: claimed=%v err=%v", claimed, err)
	}
	if claimed, err := repo.ClaimTableForTill(ctx, replica, "till-a", now.Add(-2*time.Minute)); err != nil || !claimed {
		t.Fatalf("ClaimTableForTill: claimed=%v err=%v", claimed, err)
	}

	if err := repo.ClearLocalTableClaims(ctx); err != nil {
		t.Fatalf("ClearLocalTableClaims: %v", err)
	}

	if ok, err := repo.IsTableFree(ctx, own, ""); err != nil || !ok {
		t.Errorf("the boot sweep must clear this till's OWN claim, got ok=%v err=%v", ok, err)
	}
	if owner, ok := claimTillOf(t, dbo, replica); !ok || owner != "till-a" {
		t.Fatalf("a still-running replica's claim must survive a primary reboot, got %q (ok=%v)", owner, ok)
	}
}

// ListTablesWithState (the floor plan + picker's occupancy source) must light
// a table up from a live claim alone, with the claim's timestamp as
// OccupiedSince; when a held_sales row AND a claim both reference a table
// (not a steady state, but must not produce wrong data) the earlier of the
// two wins.
func TestListTablesWithState_OccupiedViaLiveClaim(t *testing.T) {
	dbo, repo := openTablesTestDB(t)
	ctx := context.Background()

	claimedID, err := repo.CreateTable(ctx, "T1", "", 4, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	bothID, err := repo.CreateTable(ctx, "T2", "", 4, "rect", 300, 100)
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	if _, err := repo.CreateTable(ctx, "T3", "", 2, "round", 500, 100); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	if claimed, err := repo.ClaimTable(ctx, claimedID); err != nil || !claimed {
		t.Fatalf("ClaimTable T1: claimed=%v err=%v", claimed, err)
	}
	// T2: a held order from earlier AND a live claim (seeded raw so the
	// claim timestamp is deterministic and later than the held row).
	if _, err := dbo.DB.Exec(`
INSERT INTO held_sales (id, label, total_minor, line_count, payload, table_id, created_at)
VALUES ('h1','',0,0,'{}',?, '2026-08-18 10:05:00')`, bothID); err != nil {
		t.Fatalf("seed held sale: %v", err)
	}
	if _, err := dbo.DB.Exec(`INSERT INTO table_claims (table_id, claimed_at) VALUES (?, '2026-08-18T11:00:00Z')`, bothID); err != nil {
		t.Fatalf("seed claim: %v", err)
	}

	rows, err := repo.ListTablesWithState(ctx)
	if err != nil {
		t.Fatalf("ListTablesWithState: %v", err)
	}
	byLabel := map[string]TableWithState{}
	for _, r := range rows {
		byLabel[r.Label] = r
	}
	t1 := byLabel["T1"]
	if !t1.Occupied || t1.OccupiedSince == "" {
		t.Fatalf("T1 must be occupied by its live claim alone: %+v", t1)
	}
	t2 := byLabel["T2"]
	if !t2.Occupied || t2.OccupiedSince != "2026-08-18 10:05:00" {
		t.Fatalf("T2 must be occupied since the EARLIER of held row / claim: %+v", t2)
	}
	if t3 := byLabel["T3"]; t3.Occupied || t3.OccupiedSince != "" {
		t.Fatalf("T3 must stay free: %+v", t3)
	}
	if len(rows) != 3 {
		t.Fatalf("a table with both a held row and a claim must still list exactly once; got %d rows", len(rows))
	}
}

// --- ut-docs#1703: till-owned, TTL-reconciled claims ---

// seedTill inserts an enrolled till row with the given last_seen_at (empty
// = never seen, i.e. NULL) — the shape ClaimTableForTill's staleness
// subquery reads. Raw SQL is fine in tests.
func seedTill(t *testing.T, dbo *db.DB, id, lastSeenAt string) {
	t.Helper()
	if lastSeenAt == "" {
		mustExec(t, dbo, `INSERT INTO tills (id, name, bearer_hash) VALUES (?, ?, ?)`, id, "Till "+id, "hash-"+id)
		return
	}
	mustExec(t, dbo, `INSERT INTO tills (id, name, bearer_hash, last_seen_at) VALUES (?, ?, ?, ?)`, id, "Till "+id, "hash-"+id, lastSeenAt)
}

func claimTillOf(t *testing.T, dbo *db.DB, tableID string) (string, bool) {
	t.Helper()
	var tillID string
	err := dbo.DB.QueryRow(`SELECT till_id FROM table_claims WHERE table_id = ?`, tableID).Scan(&tillID)
	if err == sql.ErrNoRows {
		return "", false
	}
	if err != nil {
		t.Fatalf("read claim: %v", err)
	}
	return tillID, true
}

// A fresh claim on a free table succeeds and records the owning till; the
// same table for a SECOND till, while the first is still fresh (last_seen_at
// within the cutoff), is refused — the cross-till double-claim ut-docs#1703
// exists to stop.
func TestClaimTableForTill_FreshClaimSucceeds_SecondTillRefusedWhileOwnerFresh(t *testing.T) {
	dbo, repo := openTablesTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	seedTill(t, dbo, "till-a", now.Format(time.RFC3339))
	seedTill(t, dbo, "till-b", now.Format(time.RFC3339))

	id, err := repo.CreateTable(ctx, "T1", "", 4, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	cutoff := now.Add(-2 * time.Minute)
	claimed, err := repo.ClaimTableForTill(ctx, id, "till-a", cutoff)
	if err != nil || !claimed {
		t.Fatalf("first ClaimTableForTill: claimed=%v err=%v", claimed, err)
	}
	if owner, ok := claimTillOf(t, dbo, id); !ok || owner != "till-a" {
		t.Fatalf("claim must record the owning till, got %q (ok=%v)", owner, ok)
	}
	claimed, err = repo.ClaimTableForTill(ctx, id, "till-b", cutoff)
	if err != nil {
		t.Fatalf("second till's ClaimTableForTill must not error: %v", err)
	}
	if claimed {
		t.Fatal("a second till must not take a table a fresh till already claims")
	}
	if owner, _ := claimTillOf(t, dbo, id); owner != "till-a" {
		t.Fatalf("a refused claim must leave the owner untouched, got %q", owner)
	}
	// The owner re-claiming its own table SUCCEEDS (independent review
	// 2026-09-07 — this used to return false, see
	// TestClaimTableForTill_OwnOrphanedClaimIsRetakenAfterRestart for why
	// that permanently bricked a table). The row stays its own either way.
	claimed, err = repo.ClaimTableForTill(ctx, id, "till-a", cutoff)
	if err != nil || !claimed {
		t.Fatalf("owner re-claim: claimed=%v err=%v, want true/nil", claimed, err)
	}
	if owner, _ := claimTillOf(t, dbo, id); owner != "till-a" {
		t.Fatalf("an owner re-claim must leave the row its own, got %q", owner)
	}
	var n int
	if err := dbo.DB.QueryRow(`SELECT COUNT(*) FROM table_claims WHERE table_id = ?`, id).Scan(&n); err != nil || n != 1 {
		t.Fatalf("an owner re-claim must not duplicate the row, got %d (err %v)", n, err)
	}
}

// A till must always be able to re-take a claim it already owns — the
// blocking bug found in independent review, 2026-09-07.
//
// The TTL only expires a claim whose owning till has gone QUIET. A till that
// crashes mid-basket and reboots is loud again within seconds (every sync
// call touches tills.last_seen_at), and its boot sweep only clears its own
// LOCAL rows — so its orphan on the primary was, before this fix, immortal:
// never TTL-expired (the till is online), never released (the basket that
// would have released it died with the process), and refused to the only
// party that could ever clear it. The operator re-picks table 5 on the till
// that just rebooted and gets "occupied" forever, with no in-product
// recovery until #1393's manual free-the-table action. Same shape when a
// release write-through simply fails: the local row goes regardless (by
// design, offline-first), the primary's does not.
//
// That is a REGRESSION the write-through introduced — before ut-docs#1703 the
// boot sweep alone fully recovered this — and it defeats the card's own
// acceptance criterion that a till crashing mid-claim must not lock a table
// past a bounded TTL.
func TestClaimTableForTill_OwnOrphanedClaimIsRetakenAfterRestart(t *testing.T) {
	dbo, repo := openTablesTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	seedTill(t, dbo, "till-a", now.Format(time.RFC3339))
	seedTill(t, dbo, "till-b", now.Format(time.RFC3339))

	id, err := repo.CreateTable(ctx, "T1", "", 4, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	cutoff := now.Add(-2 * time.Minute)
	if claimed, err := repo.ClaimTableForTill(ctx, id, "till-a", cutoff); err != nil || !claimed {
		t.Fatalf("initial claim: claimed=%v err=%v", claimed, err)
	}

	// till-a crashes and reboots. It is ONLINE again (last_seen_at fresh, so
	// the TTL can never expire its row), its LOCAL claim was wiped by the
	// boot sweep, and the operator re-picks the same table.
	if claimed, err := repo.ClaimTableForTill(ctx, id, "till-a", cutoff); err != nil || !claimed {
		t.Fatalf("a till must be able to re-take its own orphaned claim, got claimed=%v err=%v", claimed, err)
	}
	if owner, ok := claimTillOf(t, dbo, id); !ok || owner != "till-a" {
		t.Fatalf("after the re-take the owner must still be till-a, got %q (ok=%v)", owner, ok)
	}

	// The re-take must not have widened into "anyone may take it": till-b,
	// with till-a live and holding the table, is still refused.
	if claimed, err := repo.ClaimTableForTill(ctx, id, "till-b", cutoff); err != nil || claimed {
		t.Fatalf("a DIFFERENT live till must still be refused, got claimed=%v err=%v", claimed, err)
	}
}

// Stale takeover: once the owning till's last_seen_at is OLDER than the
// cutoff (it stopped talking to the primary — crashed, lost network), its
// claim is expired and the second till's claim succeeds. A till never seen
// at all (last_seen_at NULL) counts as stale too.
func TestClaimTableForTill_StaleOwnerIsExpiredAndTakenOver(t *testing.T) {
	dbo, repo := openTablesTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	seedTill(t, dbo, "till-a", now.Add(-10*time.Minute).Format(time.RFC3339))
	seedTill(t, dbo, "till-b", now.Format(time.RFC3339))
	seedTill(t, dbo, "till-c", "") // never seen

	id, err := repo.CreateTable(ctx, "T1", "", 4, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	// Seed till-a's claim directly: at claim time it was fresh.
	mustExec(t, dbo, `INSERT INTO table_claims (table_id, claimed_at, till_id) VALUES (?, ?, ?)`, id, now.Add(-10*time.Minute).Format(time.RFC3339), "till-a")

	cutoff := now.Add(-2 * time.Minute)
	claimed, err := repo.ClaimTableForTill(ctx, id, "till-b", cutoff)
	if err != nil || !claimed {
		t.Fatalf("takeover of a stale till's claim: claimed=%v err=%v, want true/nil", claimed, err)
	}
	if owner, _ := claimTillOf(t, dbo, id); owner != "till-b" {
		t.Fatalf("after takeover the owner must be till-b, got %q", owner)
	}
	var n int
	if err := dbo.DB.QueryRow(`SELECT COUNT(*) FROM table_claims WHERE table_id = ?`, id).Scan(&n); err != nil || n != 1 {
		t.Fatalf("exactly one claim row must remain, got %d (err %v)", n, err)
	}

	// A never-seen till's claim (last_seen_at NULL) is equally stale.
	id2, err := repo.CreateTable(ctx, "T2", "", 4, "rect", 200, 200)
	if err != nil {
		t.Fatalf("CreateTable T2: %v", err)
	}
	mustExec(t, dbo, `INSERT INTO table_claims (table_id, claimed_at, till_id) VALUES (?, ?, ?)`, id2, now.Format(time.RFC3339), "till-c")
	claimed, err = repo.ClaimTableForTill(ctx, id2, "till-b", cutoff)
	if err != nil || !claimed {
		t.Fatalf("takeover of a never-seen till's claim: claimed=%v err=%v, want true/nil", claimed, err)
	}
	// And a claim by a till that no longer exists in `tills` at all (revoked
	// enrolment) is stale by the same rule.
	id3, err := repo.CreateTable(ctx, "T3", "", 4, "rect", 300, 300)
	if err != nil {
		t.Fatalf("CreateTable T3: %v", err)
	}
	mustExec(t, dbo, `INSERT INTO table_claims (table_id, claimed_at, till_id) VALUES (?, ?, ?)`, id3, now.Format(time.RFC3339), "till-gone")
	claimed, err = repo.ClaimTableForTill(ctx, id3, "till-b", cutoff)
	if err != nil || !claimed {
		t.Fatalf("takeover of a revoked till's claim: claimed=%v err=%v, want true/nil", claimed, err)
	}
}

// The PRIMARY's own live basket claims with till_id=” (ClaimTable, the
// unchanged local path — same this-till convention as sales.till_id). That
// row must NEVER be auto-expired by ClaimTableForTill: the primary is, by
// construction, always "online" with itself, and it has no tills row of its
// own to be judged fresh or stale against. Even a cutoff in the far future
// (everything looks stale) must leave it alone.
func TestClaimTableForTill_NeverExpiresThisTillLocalClaim(t *testing.T) {
	dbo, repo := openTablesTestDB(t)
	ctx := context.Background()
	seedTill(t, dbo, "till-b", time.Now().UTC().Format(time.RFC3339))

	id, err := repo.CreateTable(ctx, "T1", "", 4, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	if claimed, err := repo.ClaimTable(ctx, id); err != nil || !claimed {
		t.Fatalf("local ClaimTable: claimed=%v err=%v", claimed, err)
	}
	if owner, ok := claimTillOf(t, dbo, id); !ok || owner != "" {
		t.Fatalf("local ClaimTable must record till_id='', got %q (ok=%v)", owner, ok)
	}
	farFuture := time.Now().UTC().Add(365 * 24 * time.Hour)
	claimed, err := repo.ClaimTableForTill(ctx, id, "till-b", farFuture)
	if err != nil {
		t.Fatalf("ClaimTableForTill: %v", err)
	}
	if claimed {
		t.Fatal("a till_id='' (this-till-local) claim must never be expired by a cross-till claim")
	}
	if owner, ok := claimTillOf(t, dbo, id); !ok || owner != "" {
		t.Fatalf("the local claim row must survive untouched, got %q (ok=%v)", owner, ok)
	}
}

// TestClaimTableForTill_LocalOwnClaimIsRetakenAndRefreshed (ut-docs#1704,
// independent review 2026-09-07): tillID="" re-claiming a till_id=” row it
// already holds must succeed (claimed=true) and refresh claimed_at, the same
// "own-claim" re-take ClaimTableForTill already gave a REAL till id — before
// this fix, the `till_id != ”` guard blocked the own-claim disjunct from
// EVER matching tillID=” itself, so INSERT OR IGNORE silently no-op'd on
// the pre-existing row and reported claimed=false for a claim that in fact
// already stood. tillID=” is not only ever the primary's own live basket —
// claimTableWriteThrough's local fallback branch has always called this with
// tillID=” too, and a held order's claim (ut-docs#1704) is now kept alive
// through the whole park rather than re-claimed from scratch on resume, so
// this path is newly reachable against an already-self-held row.
func TestClaimTableForTill_LocalOwnClaimIsRetakenAndRefreshed(t *testing.T) {
	dbo, repo := openTablesTestDB(t)
	ctx := context.Background()

	id, err := repo.CreateTable(ctx, "T1", "", 4, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	cutoff := time.Now().UTC().Add(-2 * time.Minute)
	claimed, err := repo.ClaimTableForTill(ctx, id, "", cutoff)
	if err != nil || !claimed {
		t.Fatalf("initial local claim: claimed=%v err=%v", claimed, err)
	}
	var firstClaimedAt string
	if err := dbo.QueryRow(`SELECT claimed_at FROM table_claims WHERE table_id = ?`, id).Scan(&firstClaimedAt); err != nil {
		t.Fatalf("read claimed_at: %v", err)
	}
	time.Sleep(1100 * time.Millisecond) // RFC3339 has second resolution

	claimed, err = repo.ClaimTableForTill(ctx, id, "", cutoff)
	if err != nil {
		t.Fatalf("re-claim: %v", err)
	}
	if !claimed {
		t.Fatal("re-claiming a till_id='' row this same local caller already holds must report claimed=true, not silently no-op")
	}
	owner, ok := claimTillOf(t, dbo, id)
	if !ok || owner != "" {
		t.Fatalf("row must still be the local till_id='' row, got %q (ok=%v)", owner, ok)
	}
	var secondClaimedAt string
	if err := dbo.QueryRow(`SELECT claimed_at FROM table_claims WHERE table_id = ?`, id).Scan(&secondClaimedAt); err != nil {
		t.Fatalf("read claimed_at: %v", err)
	}
	if secondClaimedAt == firstClaimedAt {
		t.Fatalf("re-taking the row must refresh claimed_at, got the same value %q both times", firstClaimedAt)
	}

	// And it must not have widened into "any tillID may take it": a
	// DIFFERENT till is still refused while this local claim is fresh.
	if claimed, err := repo.ClaimTableForTill(ctx, id, "till-other", cutoff); err != nil || claimed {
		t.Fatalf("a different till must still be refused, claimed=%v err=%v", claimed, err)
	}
}

// ReleaseTableClaimForTill deletes only the calling till's own claim: another
// till's row (or the primary's own local ” row) is left alone, and releasing
// with nothing to release is a no-op, not an error — same convention as
// ReleaseTableClaim.
func TestReleaseTableClaimForTill_OnlyDeletesOwnClaim(t *testing.T) {
	dbo, repo := openTablesTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339)
	seedTill(t, dbo, "till-a", now)
	seedTill(t, dbo, "till-b", now)

	id, err := repo.CreateTable(ctx, "T1", "", 4, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	if err := repo.ReleaseTableClaimForTill(ctx, id, "till-a"); err != nil {
		t.Fatalf("release with nothing to release must be a no-op, got: %v", err)
	}
	if claimed, err := repo.ClaimTableForTill(ctx, id, "till-a", time.Now().Add(-2*time.Minute)); err != nil || !claimed {
		t.Fatalf("ClaimTableForTill: claimed=%v err=%v", claimed, err)
	}
	// Another till releasing: not its claim, nothing happens.
	if err := repo.ReleaseTableClaimForTill(ctx, id, "till-b"); err != nil {
		t.Fatalf("ReleaseTableClaimForTill (other till): %v", err)
	}
	if owner, ok := claimTillOf(t, dbo, id); !ok || owner != "till-a" {
		t.Fatalf("another till's release must not touch the claim, got %q (ok=%v)", owner, ok)
	}
	// The owner releasing: gone.
	if err := repo.ReleaseTableClaimForTill(ctx, id, "till-a"); err != nil {
		t.Fatalf("ReleaseTableClaimForTill (owner): %v", err)
	}
	if _, ok := claimTillOf(t, dbo, id); ok {
		t.Fatal("the owner's release must delete the claim")
	}
	// The primary's own local claim ('' till) is not deletable via a till id.
	if claimed, err := repo.ClaimTable(ctx, id); err != nil || !claimed {
		t.Fatalf("local ClaimTable: claimed=%v err=%v", claimed, err)
	}
	if err := repo.ReleaseTableClaimForTill(ctx, id, "till-a"); err != nil {
		t.Fatalf("ReleaseTableClaimForTill vs local claim: %v", err)
	}
	if owner, ok := claimTillOf(t, dbo, id); !ok || owner != "" {
		t.Fatalf("a till-scoped release must never delete the local '' claim, got %q (ok=%v)", owner, ok)
	}
}

// TestReleaseAllTableClaimsForTill_DropsOnlyThatTillsClaims (ut-docs#1712):
// the boot-time "release everything of mine" primitive behind POST
// /api/sync/tables/release-all. Unlike ReleaseTableClaimForTill (one table,
// called by the owning basket's own release/hold/move lifecycle), this must
// drop EVERY table the calling till holds in one call — the residual
// ut-docs#1703's own review left open (finding 7): a till that reboots and
// never revisits a specific table again keeps its orphan on the primary
// forever, because ClaimTableForTill's staleness check only ever runs when
// someone attempts a NEW claim on that SAME table, and the till looks
// "online" again the moment it talks to the primary about anything at all.
func TestReleaseAllTableClaimsForTill_DropsOnlyThatTillsClaims(t *testing.T) {
	dbo, repo := openTablesTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339)
	seedTill(t, dbo, "till-a", now)
	seedTill(t, dbo, "till-b", now)

	a1, err := repo.CreateTable(ctx, "T1", "", 4, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable T1: %v", err)
	}
	a2, err := repo.CreateTable(ctx, "T2", "", 4, "rect", 200, 100)
	if err != nil {
		t.Fatalf("CreateTable T2: %v", err)
	}
	b1, err := repo.CreateTable(ctx, "T3", "", 4, "rect", 300, 100)
	if err != nil {
		t.Fatalf("CreateTable T3: %v", err)
	}
	local, err := repo.CreateTable(ctx, "T4", "", 4, "rect", 400, 100)
	if err != nil {
		t.Fatalf("CreateTable T4: %v", err)
	}
	if claimed, err := repo.ClaimTableForTill(ctx, a1, "till-a", time.Now().Add(-2*time.Minute)); err != nil || !claimed {
		t.Fatalf("claim a1 for till-a: claimed=%v err=%v", claimed, err)
	}
	if claimed, err := repo.ClaimTableForTill(ctx, a2, "till-a", time.Now().Add(-2*time.Minute)); err != nil || !claimed {
		t.Fatalf("claim a2 for till-a: claimed=%v err=%v", claimed, err)
	}
	if claimed, err := repo.ClaimTableForTill(ctx, b1, "till-b", time.Now().Add(-2*time.Minute)); err != nil || !claimed {
		t.Fatalf("claim b1 for till-b: claimed=%v err=%v", claimed, err)
	}
	// The primary's own local claim ('' till) must never be touched by a
	// till-scoped release-all, same invariant ReleaseTableClaimForTill pins.
	if claimed, err := repo.ClaimTable(ctx, local); err != nil || !claimed {
		t.Fatalf("local ClaimTable: claimed=%v err=%v", claimed, err)
	}

	if err := repo.ReleaseAllTableClaimsForTill(ctx, "till-a", nil); err != nil {
		t.Fatalf("ReleaseAllTableClaimsForTill: %v", err)
	}

	if _, ok := claimTillOf(t, dbo, a1); ok {
		t.Error("till-a's claim on T1 must be gone")
	}
	if _, ok := claimTillOf(t, dbo, a2); ok {
		t.Error("till-a's claim on T2 must be gone")
	}
	if owner, ok := claimTillOf(t, dbo, b1); !ok || owner != "till-b" {
		t.Fatalf("till-b's claim on T3 must survive till-a's release-all, got %q (ok=%v)", owner, ok)
	}
	if owner, ok := claimTillOf(t, dbo, local); !ok || owner != "" {
		t.Fatalf("the primary's own local claim on T4 must survive, got %q (ok=%v)", owner, ok)
	}

	// Idempotent — nothing left to release, still no error.
	if err := repo.ReleaseAllTableClaimsForTill(ctx, "till-a", nil); err != nil {
		t.Fatalf("repeat ReleaseAllTableClaimsForTill: %v", err)
	}
}

// TestReleaseAllTableClaimsForTill_KeepListSurvivesHeldOrders (ut-docs#1712,
// independent review 2026-09-07, blocker 1): a held order's table_claims row
// is deliberately kept alive through the whole park (ut-docs#1704) and DOES
// survive a restart (held_sales is durable) — unlike a live basket's, which
// never does. An unconditional release-all would delete that row too and
// rely on a best-effort network re-claim to restore it, with a real window
// where the table sits genuinely unclaimed on the primary. keepTableIDs is
// the fix: a table in the list is untouched even though it belongs to the
// same till being released.
func TestReleaseAllTableClaimsForTill_KeepListSurvivesHeldOrders(t *testing.T) {
	dbo, repo := openTablesTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339)
	seedTill(t, dbo, "till-a", now)

	held, err := repo.CreateTable(ctx, "T1", "", 4, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable T1: %v", err)
	}
	orphan, err := repo.CreateTable(ctx, "T2", "", 4, "rect", 200, 100)
	if err != nil {
		t.Fatalf("CreateTable T2: %v", err)
	}
	if claimed, err := repo.ClaimTableForTill(ctx, held, "till-a", time.Now().Add(-2*time.Minute)); err != nil || !claimed {
		t.Fatalf("claim held table for till-a: claimed=%v err=%v", claimed, err)
	}
	if claimed, err := repo.ClaimTableForTill(ctx, orphan, "till-a", time.Now().Add(-2*time.Minute)); err != nil || !claimed {
		t.Fatalf("claim orphan table for till-a: claimed=%v err=%v", claimed, err)
	}

	if err := repo.ReleaseAllTableClaimsForTill(ctx, "till-a", []string{held}); err != nil {
		t.Fatalf("ReleaseAllTableClaimsForTill: %v", err)
	}

	if owner, ok := claimTillOf(t, dbo, held); !ok || owner != "till-a" {
		t.Fatalf("a table in the keep list must survive, got %q (ok=%v)", owner, ok)
	}
	if _, ok := claimTillOf(t, dbo, orphan); ok {
		t.Error("a table NOT in the keep list must still be released")
	}
}

// TestReleaseAllTableClaimsForTill_EmptyTillIDIsNoOp: the "" till id is the
// PRIMARY's own local-claim convention everywhere else in this file
// (ClaimTable/ReleaseTableClaim) — this method's only caller (the sync
// handler) always supplies a real till's bearer-resolved id, but a bare
// unscoped DELETE would otherwise let an empty id silently wipe every till's
// local claim in the shop. Guarded explicitly rather than relying on the
// caller never getting it wrong.
func TestReleaseAllTableClaimsForTill_EmptyTillIDIsNoOp(t *testing.T) {
	dbo, repo := openTablesTestDB(t)
	ctx := context.Background()
	id, err := repo.CreateTable(ctx, "T1", "", 4, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	if claimed, err := repo.ClaimTable(ctx, id); err != nil || !claimed {
		t.Fatalf("local ClaimTable: claimed=%v err=%v", claimed, err)
	}
	if err := repo.ReleaseAllTableClaimsForTill(ctx, "", nil); err != nil {
		t.Fatalf("ReleaseAllTableClaimsForTill(\"\"): %v", err)
	}
	if owner, ok := claimTillOf(t, dbo, id); !ok || owner != "" {
		t.Fatalf("an empty till id must never release the local '' claim, got %q (ok=%v)", owner, ok)
	}
}
