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
	"path/filepath"
	"testing"

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

// ClearAllTableClaims is the boot-time recovery for a claim orphaned by an
// unclean shutdown (independent review finding, ut-docs#1390): pos.Service
// always starts empty, so any table_claims row still present at Init belongs
// to a process that never released it. Without this sweep, that table would
// stay unbookable forever — nothing else ever revisits an orphaned row.
func TestClearAllTableClaims_WipesExistingRowsAndIsSafeOnEmpty(t *testing.T) {
	_, repo := openTablesTestDB(t)
	ctx := context.Background()

	// Safe no-op with nothing to clear (e.g. a clean-shutdown boot).
	if err := repo.ClearAllTableClaims(ctx); err != nil {
		t.Fatalf("ClearAllTableClaims on empty table: %v", err)
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

	if err := repo.ClearAllTableClaims(ctx); err != nil {
		t.Fatalf("ClearAllTableClaims: %v", err)
	}

	if ok, err := repo.IsTableFree(ctx, t1, ""); err != nil || !ok {
		t.Errorf("T1 must be free after ClearAllTableClaims, got ok=%v err=%v", ok, err)
	}
	if ok, err := repo.IsTableFree(ctx, t2, ""); err != nil || !ok {
		t.Errorf("T2 must be free after ClearAllTableClaims, got ok=%v err=%v", ok, err)
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
