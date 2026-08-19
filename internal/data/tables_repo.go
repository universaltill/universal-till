package data

// Table floor plan (universaltill/ut-docs#814, ADR-0054): per-store dining
// tables with a persisted position on a fixed 1000×1000 logical canvas.
// Tables are soft-disabled (enabled=0), never hard-deleted, mirroring
// kitchen stations — order history may reference them once ut-docs#820
// wires table assignment onto held sales.
//
// ListTablesWithState is the live free/occupied query. It LEFT JOINs
// held_sales.table_id — the column migration 054 added forward-compatibly
// for #820. Until #820 ships nothing writes that column, so every table
// legitimately resolves free; the join is real (and tested) so occupancy
// lights up the moment #820 starts writing it, with no change here.

import (
	"context"
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

// ListTablesWithState returns every table with its live free/occupied state:
// occupied when at least one open (held) order carries its table_id, with
// the oldest such order's created_at as OccupiedSince. Pre-#820 nothing
// writes held_sales.table_id, so every table resolves free — the correct,
// honest state, not a stub.
func (r *POSRepo) ListTablesWithState(ctx context.Context) ([]TableWithState, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT t.id, t.label, t.area_zone, t.seat_count, t.shape, t.pos_x, t.pos_y, t.enabled,
       t.created_at, t.updated_at, COALESCE(MIN(h.created_at), '')
FROM tables t
LEFT JOIN held_sales h ON h.table_id = t.id
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
