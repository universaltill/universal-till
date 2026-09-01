package data

// Table floor plan (universaltill/ut-docs#814, ADR-0054): per-store dining
// tables with a persisted position on a fixed 1000×1000 logical canvas.
// Tables are soft-disabled (enabled=0), never hard-deleted, mirroring
// kitchen stations — order history may reference them once ut-docs#820
// wires table assignment onto held sales.
//
// ListTablesWithState is the live free/occupied query. It LEFT JOINs
// held_sales.table_id — the column migration 054 added forward-compatibly
// for #820 — and, since ut-docs#1390, table_claims: the live (not-yet-held)
// basket's own pick, persisted the moment it's made (migration 077) so the
// next order on the same till can't take the same table. IsTableFree
// consults both sources the same way; ClaimTable/ReleaseTableClaim are the
// claim's only writers.

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// TableCanvasSize is the fixed logical canvas the floor plan is laid out on
// (ADR-0054): positions are integers in 0..TableCanvasSize on both axes, and
// the SVG viewBox scales that same layout to any physical screen.
const TableCanvasSize = 1000

// TableEdgeInset keeps a table's PERSISTED CENTRE far enough from the canvas
// edge that its own shape never clips against the SVG viewBox — a table
// dragged to the extreme corner previously rendered three-quarters off-plan
// (2026-08-19 code review, ut-docs#814). The largest shape footprint is the
// rect's own half-width (web/ui/partials/tables_state.html: x="-65"
// width="130"); the round shape's radius (55) fits comfortably inside the
// same inset, so one shared value covers both without per-shape branching.
const TableEdgeInset = 65

// Table is one dining table on the floor plan.
type Table struct {
	ID        string
	Label     string // table number or name ("T1", "Window 2")
	AreaZone  string // free-text area/zone ("Terrace"); may be empty
	SeatCount int
	Shape     string // 'rect' | 'round'
	PosX      int    // logical-canvas units, 0..TableCanvasSize
	PosY      int
	Enabled   bool
	CreatedAt string
	UpdatedAt string
}

// TableWithState is a table plus its live order state. OccupiedSince is the
// raw created_at of the OLDEST open (held) order assigned to the table —
// exactly as stored ("2006-01-02 15:04:05" from the held_sales schema
// default, or RFC3339 if a writer sets it explicitly); empty when free.
type TableWithState struct {
	Table
	Occupied      bool
	OccupiedSince string
}

func validTableShape(shape string) bool { return shape == "rect" || shape == "round" }

// clampToCanvas pins a coordinate into the logical canvas — the single place
// that owns the bound, so no client-supplied position can land off-plan.
func clampToCanvas(v int) int {
	if v < TableEdgeInset {
		return TableEdgeInset
	}
	if v > TableCanvasSize-TableEdgeInset {
		return TableCanvasSize - TableEdgeInset
	}
	return v
}

// ListTables returns every table (enabled and disabled) for the floor-plan
// editor, ordered by area then label.
func (r *POSRepo) ListTables(ctx context.Context) ([]Table, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, label, area_zone, seat_count, shape, pos_x, pos_y, enabled, created_at, updated_at
FROM tables ORDER BY area_zone, label`)
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	defer rows.Close()
	var out []Table
	for rows.Next() {
		var t Table
		var enabled int
		if err := rows.Scan(&t.ID, &t.Label, &t.AreaZone, &t.SeatCount, &t.Shape, &t.PosX, &t.PosY, &enabled, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan table: %w", err)
		}
		t.Enabled = enabled == 1
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tables: %w", err)
	}
	return out, nil
}

// CreateTable adds a new, enabled table at the given canvas position.
func (r *POSRepo) CreateTable(ctx context.Context, label, areaZone string, seatCount int, shape string, posX, posY int) (string, error) {
	if !validTableShape(shape) {
		return "", fmt.Errorf("create table: invalid shape %q", shape)
	}
	if seatCount < 0 {
		seatCount = 0
	}
	id := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := r.db.ExecContext(ctx, `
INSERT INTO tables (id, label, area_zone, seat_count, shape, pos_x, pos_y, enabled, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`,
		id, label, areaZone, seatCount, shape, clampToCanvas(posX), clampToCanvas(posY), now, now); err != nil {
		return "", fmt.Errorf("create table: %w", err)
	}
	return id, nil
}

// UpdateTable changes a table's label, area/zone, seat count and shape; its
// id and saved position are unaffected (position has its own write path,
// SetTablePosition, so an attribute edit never disturbs the layout).
func (r *POSRepo) UpdateTable(ctx context.Context, id, label, areaZone string, seatCount int, shape string) error {
	if !validTableShape(shape) {
		return fmt.Errorf("update table: invalid shape %q", shape)
	}
	if seatCount < 0 {
		seatCount = 0
	}
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := r.db.ExecContext(ctx, `
UPDATE tables SET label = ?, area_zone = ?, seat_count = ?, shape = ?, updated_at = ? WHERE id = ?`,
		label, areaZone, seatCount, shape, now, id)
	if err != nil {
		return fmt.Errorf("update table: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("update table: %s not found", id)
	}
	return nil
}

// SetTablePosition persists a drag-to-place move. Coordinates are clamped to
// the logical canvas here, not trusted from the client.
func (r *POSRepo) SetTablePosition(ctx context.Context, id string, posX, posY int) error {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := r.db.ExecContext(ctx, `
UPDATE tables SET pos_x = ?, pos_y = ?, updated_at = ? WHERE id = ?`,
		clampToCanvas(posX), clampToCanvas(posY), now, id)
	if err != nil {
		return fmt.Errorf("set table position: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("set table position: %s not found", id)
	}
	return nil
}

// SetTableEnabled soft-disables/re-enables a table, mirroring
// SetKitchenStationEnabled — no hard delete, so order rows referencing the
// table (once #820 writes them) never orphan.
func (r *POSRepo) SetTableEnabled(ctx context.Context, id string, enabled bool) error {
	v := 0
	if enabled {
		v = 1
	}
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := r.db.ExecContext(ctx, `
UPDATE tables SET enabled = ?, updated_at = ? WHERE id = ?`, v, now, id)
	if err != nil {
		return fmt.Errorf("set table enabled: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("set table enabled: %s not found", id)
	}
	return nil
}

// GetTable looks up a single table by id — the label-resolution lookup a
// table-assignment handler (ut-docs#820) uses so pos.Service.SetTable can
// be given the table's current label without the caller re-deriving it.
func (r *POSRepo) GetTable(ctx context.Context, id string) (Table, bool, error) {
	var t Table
	var enabled int
	err := r.db.QueryRowContext(ctx, `
SELECT id, label, area_zone, seat_count, shape, pos_x, pos_y, enabled, created_at, updated_at
FROM tables WHERE id = ?`, id).Scan(&t.ID, &t.Label, &t.AreaZone, &t.SeatCount, &t.Shape, &t.PosX, &t.PosY, &enabled, &t.CreatedAt, &t.UpdatedAt)
	if err == sql.ErrNoRows {
		return Table{}, false, nil
	}
	if err != nil {
		return Table{}, false, fmt.Errorf("get table: %w", err)
	}
	t.Enabled = enabled == 1
	return t, true, nil
}

// IsTableFree reports whether id has neither an open (held) order assigned
// to it nor a live-basket claim (table_claims, ut-docs#1390) — the check a
// "move this order to a different table" handler (ut-docs#820) and the
// live basket's own table pick must pass before accepting the assignment,
// so an order can never silently land on a table another order already
// occupies. excludeHeldSaleID is the held sale BEING moved: it may already
// legitimately hold the FROM table (or, moving between two of its own past
// holds, could otherwise self-block a no-op move onto its own current
// table), so its own row is excluded from the occupancy check. Pass ""
// when there is no held sale to exclude (e.g. assigning a table to a live,
// not-yet-held basket). It excludes a held_sales row ONLY — never a live
// claim: there is one live basket per till, and its handler short-circuits
// a re-pick of its own current table before ever asking, so a claim seen
// here is by construction someone else's.
func (r *POSRepo) IsTableFree(ctx context.Context, id string, excludeHeldSaleID string) (bool, error) {
	var occupied int
	err := r.db.QueryRowContext(ctx, `
SELECT (SELECT COUNT(*) FROM held_sales WHERE table_id = ? AND id != ?)
     + (SELECT COUNT(*) FROM table_claims WHERE table_id = ?)`,
		id, excludeHeldSaleID, id).Scan(&occupied)
	if err != nil {
		return false, fmt.Errorf("is table free: %w", err)
	}
	return occupied == 0, nil
}

// ClaimTable reserves tableID for the live basket (ut-docs#1390) by
// inserting its table_claims row. claimed reports whether THIS call took
// the claim: false means the row already existed — the table is occupied
// by another live basket's claim — which is an ordinary outcome for the
// caller to render as "occupied", not an error. INSERT OR IGNORE on the
// PRIMARY KEY makes this the race-free primitive: no check-then-insert
// window, two concurrent claims on one table can never both succeed. A
// tableID that isn't a real table fails the REFERENCES tables(id)
// constraint and IS an error (OR IGNORE does not swallow foreign-key
// violations) — callers resolve the id via GetTable first, same as the
// existing /api/pos/table handler already did for the label.
func (r *POSRepo) ClaimTable(ctx context.Context, tableID string) (claimed bool, err error) {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := r.db.ExecContext(ctx, `
INSERT OR IGNORE INTO table_claims (table_id, claimed_at) VALUES (?, ?)`, tableID, now)
	if err != nil {
		return false, fmt.Errorf("claim table: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("claim table: %w", err)
	}
	return n == 1, nil
}

// ReleaseTableClaim drops the live-basket claim on tableID (ut-docs#1390):
// the basket cleared its table, was parked (the held_sales row now carries
// the occupancy), tendered, or was reset. A no-op — not an error — when
// there is no claim to release, same convention as HeldSalesRepo.Delete on
// a missing row, so every release site can call it unconditionally.
func (r *POSRepo) ReleaseTableClaim(ctx context.Context, tableID string) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM table_claims WHERE table_id = ?`, tableID); err != nil {
		return fmt.Errorf("release table claim: %w", err)
	}
	return nil
}

// ClearAllTableClaims wipes every live-basket claim (ut-docs#1390) — called
// once at process boot (internal/pages.Init), never from a request handler.
// A table_claims row means "some live basket has this table picked right
// now", and at boot there IS no live basket yet: pos.Service starts empty,
// by construction, on every process start. So any row still present
// belongs to a process that ended without releasing it (a crash, a kill,
// a power loss) — every one is stale, unconditionally, with no per-row
// judgement call needed. Without this, a table claimed right before an
// unclean shutdown stays occupied forever: nothing else ever revisits an
// orphaned row (the picker filters occupied tables out, /api/pos/table and
// /api/pos/held/table both reject a pick on one), and until #1393 ships a
// manual "free the table" action, there is no in-product recovery at all.
// held_sales is NOT touched here — a parked order surviving a restart is
// the intended offline-first durability held_sales exists for, not a
// leftover to clear.
func (r *POSRepo) ClearAllTableClaims(ctx context.Context) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM table_claims`); err != nil {
		return fmt.Errorf("clear all table claims: %w", err)
	}
	return nil
}

// ListTablesWithState returns every table with its live free/occupied state:
// occupied when at least one open (held) order carries its table_id OR the
// live basket holds a claim on it (table_claims, ut-docs#1390), with the
// earliest such timestamp as OccupiedSince — the oldest open order's
// created_at, or the claim's claimed_at. Both sources are UNIONed before
// the LEFT JOIN so a table still lists exactly once however many rows
// reference it; MIN across the union covers the (non-steady-state) case of
// a held row and a claim both pointing at one table without crashing or
// double-listing. Both timestamp shapes ("2006-01-02 15:04:05" from the
// held_sales default, RFC3339 from claims and explicit writers) parse on
// the consuming side (tables_page.go's elapsedMinutes); MIN compares them
// as raw text, which is exact within one shape and only approximate
// across the two — acceptable for a state that should not co-exist.
func (r *POSRepo) ListTablesWithState(ctx context.Context) ([]TableWithState, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT t.id, t.label, t.area_zone, t.seat_count, t.shape, t.pos_x, t.pos_y, t.enabled,
       t.created_at, t.updated_at, COALESCE(MIN(o.since), '')
FROM tables t
LEFT JOIN (
    SELECT table_id, created_at AS since FROM held_sales WHERE table_id IS NOT NULL
    UNION ALL
    SELECT table_id, claimed_at AS since FROM table_claims
) o ON o.table_id = t.id
GROUP BY t.id
ORDER BY t.area_zone, t.label`)
	if err != nil {
		return nil, fmt.Errorf("list tables with state: %w", err)
	}
	defer rows.Close()
	var out []TableWithState
	for rows.Next() {
		var t TableWithState
		var enabled int
		if err := rows.Scan(&t.ID, &t.Label, &t.AreaZone, &t.SeatCount, &t.Shape, &t.PosX, &t.PosY,
			&enabled, &t.CreatedAt, &t.UpdatedAt, &t.OccupiedSince); err != nil {
			return nil, fmt.Errorf("scan table state: %w", err)
		}
		t.Enabled = enabled == 1
		t.Occupied = t.OccupiedSince != ""
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate table states: %w", err)
	}
	return out, nil
}
