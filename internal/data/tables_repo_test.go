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
