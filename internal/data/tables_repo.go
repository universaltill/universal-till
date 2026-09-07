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
// consults both sources the same way. The claim's writers are ClaimTable /
// ReleaseTableClaim (this till's own basket, till_id = ”) and, since
// ut-docs#1703, ClaimTableForTill / ReleaseTableClaimForTill — the primary
// side of the cross-till write-through, where a claim is owned by the
// tills.id that took it and is TTL-reconciled against tills.last_seen_at.

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
// not-yet-held basket). It excludes a held_sales row ONLY — never a claim.
// The live basket's own table pick can rely on that unconditionally (its
// handler short-circuits a re-pick of its own current table before ever
// asking, so any claim it sees here is by construction someone else's) —
// but since ut-docs#1704 a HELD order's own table_claims row also persists
// through the whole park, so a caller checking a held sale's OWN current
// table (not just its held_sales row) must short-circuit that case itself
// first, the same way, or this will wrongly read it as occupied by "someone
// else." hold_api.go's held/table move handler is the one other caller in
// this position and does exactly that.
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

// ReleaseTableClaim drops THIS till's own local live-basket claim on tableID
// (ut-docs#1390): the basket cleared its table, was tendered, or was reset.
// Parking the order no longer releases it (ut-docs#1704 — the held_sales row
// carries the occupancy TOO, but the still-live claim is what a replica has
// already write-through'd to the primary, so keeping it is what makes the
// parked order visible cross-till); hold_api.go's held/table move handler is
// the one place that releases a held order's claim now, when the table
// itself changes. A no-op — not an error — when there is no claim to
// release, same convention as HeldSalesRepo.Delete on a missing row, so
// every release site can call it unconditionally.
//
// Scoped to till_id = ” (independent review finding, ut-docs#1393) — its
// one caller, releaseTableClaimWriteThrough, always means "release MY OWN
// local claim," and till_id=” is that local row's convention (ClaimTable).
// Before ForceReleaseTableClaim existed, an unscoped delete-by-table_id was
// equivalent: the PRIMARY KEY on table_id allows at most one row per table,
// so whoever was releasing could only ever be deleting their own row.
// ForceReleaseTableClaim breaks that assumption on purpose (a manager can
// clear a claim regardless of ownership), which reopens it: without this
// scoping, a manager freeing a table whose owning till's basket still
// thinks it holds it (the till is never told), followed by a DIFFERENT
// till legitimately claiming the same table, followed by the first till's
// own eventual release call landing here — would delete the second till's
// live claim instead of the (already gone) first till's row, silently
// reproducing the cross-till double-claim ut-docs#1703 closed. See
// TestReleaseTableClaim_NeverTouchesAnotherTillsClaim.
func (r *POSRepo) ReleaseTableClaim(ctx context.Context, tableID string) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM table_claims WHERE table_id = ? AND till_id = ''`, tableID); err != nil {
		return fmt.Errorf("release table claim: %w", err)
	}
	return nil
}

// ForceReleaseTableClaim is the manager-initiated "Free table" action's
// primitive (ut-docs#1393): an unconditional drop of tableID's live-basket
// claim, regardless of which till (if any) owns it. Every other release in
// this file is triggered by the OWNING basket's own lifecycle (clear, hold,
// tender, reset) or, for ClearLocalTableClaims/ClaimTableForTill, only by
// that same till acting again — nothing clears a claim a till simply never
// revisits (a crash between claiming a table and completing/clearing the
// sale, or a replica that goes quiet and is never re-claimed). Until this
// action shipped there was no in-product recovery for that case at all —
// see tables_repo_test.go's TestForceReleaseTableClaim and the design note
// on ClearLocalTableClaims above.
//
// released reports whether a claim actually existed to drop (false is a
// safe, ordinary outcome for an already-free table — same no-op convention
// as ReleaseTableClaim). stillHeld reports whether a held_sales row is
// STILL attached to the table afterwards: a genuine held order is never
// touched here, so a table with a real parked order correctly reads
// occupied again immediately — the caller uses stillHeld to tell the
// manager that clearing the claim did not fully free the table, rather than
// silently discarding a real order to make the floor plan look free.
// A transaction, not two independent statements (independent review
// finding, ut-docs#1393): with two separate calls, a failure on the
// held_sales read after the DELETE had already committed would report an
// error — "could not free the table" — for a claim that WAS actually
// dropped, and the caller's audit write (gated on err == nil) would never
// record that real state change. Wrapping both in one transaction makes
// the two outcomes exactly consistent: either the claim was dropped AND
// stillHeld reflects reality, or nothing changed and the reported error is
// accurate.
func (r *POSRepo) ForceReleaseTableClaim(ctx context.Context, tableID string) (released bool, stillHeld bool, err error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, false, fmt.Errorf("force release table claim: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `DELETE FROM table_claims WHERE table_id = ?`, tableID)
	if err != nil {
		return false, false, fmt.Errorf("force release table claim: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, false, fmt.Errorf("force release table claim: %w", err)
	}
	var heldCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM held_sales WHERE table_id = ?`, tableID).Scan(&heldCount); err != nil {
		return false, false, fmt.Errorf("force release table claim: check held sales: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, false, fmt.Errorf("force release table claim: commit: %w", err)
	}
	return n > 0, heldCount > 0, nil
}

// ClaimTableForTill reserves tableID on behalf of an enrolled replica till
// (ut-docs#1703) — the PRIMARY-side write behind POST /api/sync/tables/claim,
// so the whole shop holds at most one live claim per table. Same race-free
// INSERT OR IGNORE primitive as ClaimTable, with the owning tills.id recorded
// in till_id (migration 008), and one thing more, in the same transaction
// first: a claim already on the table is dropped when it is
//
//   - the CALLING till's own claim — re-claiming a table you already hold
//     always succeeds, see below; or
//   - a claim belonging to a till NOT seen by the primary since cutoff
//     (tills.last_seen_at older than it, NULL, or the till no longer enrolled
//     at all) — that till crashed or lost the network mid-claim and nothing
//     else would ever clean its row up (a till's boot-time
//     ClearLocalTableClaims only ever clears its OWN local rows).
//
// The caller derives cutoff from the same 2-minute online bound the sync
// status chip uses (internal/pages tillClaimTTL).
//
// The own-claim case is NOT a nicety — without it a till permanently locks a
// table against ITSELF (independent review, 2026-09-07). Its claim survives on
// the primary whenever the release never lands: the till crashed mid-basket,
// or the release write-through hit an unreachable primary (the local row is
// dropped regardless, by design). At the next boot the till clears its LOCAL
// rows and starts talking to the primary again — so it is "online", so the
// staleness rule above can never expire its orphan, so re-picking that table
// would be refused forever. Re-taking your own row (rather than merely
// reporting claimed=true and leaving the old row) also refreshes claimed_at,
// which is what the floor plan renders as "occupied since". Safe regardless
// of how many OTHER tables this same till owns claims on (since ut-docs#1704
// that can be more than one — its live basket's pick plus one per parked
// order): the delete-then-reinsert only ever touches THIS ONE table_id, and
// a till can own at most one row per table_id (the PK), so replacing tillID's
// existing row on tableID with a fresh one for the SAME (table, till) pair
// can never disturb any of this till's other claims.
//
// A till_id = ” row — THIS till's own local claim (ClaimTable, mirroring
// sales.till_id's this-till convention) — is never expired here: the primary
// is always online with itself and has no tills row to be judged against.
// claimed=false means the table is (still) held by a DIFFERENT, live till: an
// ordinary "occupied" outcome for the caller to render, not an error.
//
// tillID=” is not only ever the PRIMARY calling on its own behalf, either
// (ut-docs#1704): claimTableWriteThrough's LOCAL fallback branch
// (tables_claim_proxy.go) has always called this with tillID=” too, on a
// standalone till or a replica with no reachable primary. There the "own-
// claim" case above applies exactly the same way: re-claiming a till_id=”
// row this same local caller already holds succeeds and refreshes
// claimed_at, rather than silently no-oping and reporting claimed=false for
// a claim that in fact already stands — which matters now that a held
// order's claim (ut-docs#1704) is kept alive through the whole park rather
// than released and re-taken from scratch, so resume can hit an
// already-self-held row here for the first time.
func (r *POSRepo) ClaimTableForTill(ctx context.Context, tableID, tillID string, cutoff time.Time) (claimed bool, err error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("claim table for till: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// The `till_id != ''` guard is scoped to the STALENESS disjunct only
	// (ut-docs#1704 fix — independent review, 2026-09-07): it must never
	// let a till_id='' row be judged against `tills.last_seen_at` (it has
	// no tills row to be judged against, so it would always look "stale"
	// and be wiped) — but it must NOT also block the plain own-claim
	// disjunct from matching tillID='' itself. That second effect was
	// unintentional: tillID='' is not only "the primary's own live
	// basket" (the case this comment originally described, when a real
	// till's id is always a uuid and this function's only caller was the
	// primary-side HTTP handler) — claimTableWriteThrough's LOCAL fallback
	// branch (tables_claim_proxy.go) has always called this with tillID=""
	// too, and hold_api.go's resume handler (ut-docs#1704) is the first
	// caller that can hit it while a till_id='' row it already owns is
	// still there (a held order's table_claims row is now kept alive
	// through the whole park, not re-claimed from scratch). Without this,
	// INSERT OR IGNORE below silently no-ops on the pre-existing row and
	// reports claimed=false for a claim that in fact already holds.
	if _, err := tx.ExecContext(ctx, `
DELETE FROM table_claims
WHERE table_id = ?
  AND (till_id = ?
       OR (till_id != '' AND till_id NOT IN (SELECT id FROM tills WHERE last_seen_at IS NOT NULL AND last_seen_at >= ?)))`,
		tableID, tillID, cutoff.UTC().Format(time.RFC3339)); err != nil {
		return false, fmt.Errorf("claim table for till: expire stale: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO table_claims (table_id, claimed_at, till_id) VALUES (?, ?, ?)`, tableID, now, tillID)
	if err != nil {
		return false, fmt.Errorf("claim table for till: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("claim table for till: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("claim table for till: commit: %w", err)
	}
	return n == 1, nil
}

// ReleaseTableClaimForTill drops tillID's own claim on tableID (ut-docs#1703)
// — the PRIMARY-side write behind POST /api/sync/tables/release. Scoped to
// the calling till: another till's claim, or this till's own ” local row,
// is untouched. A no-op — not an error — when there is nothing to release,
// same convention as ReleaseTableClaim.
func (r *POSRepo) ReleaseTableClaimForTill(ctx context.Context, tableID, tillID string) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM table_claims WHERE table_id = ? AND till_id = ?`, tableID, tillID); err != nil {
		return fmt.Errorf("release table claim for till: %w", err)
	}
	return nil
}

// ClearLocalTableClaims wipes THIS till's own live-basket claims — every
// till_id = ” row (ut-docs#1390) — called once at process boot
// (internal/pages.Init), never from a request handler. A table_claims row
// means "some live basket has this table picked right now", and at boot
// there IS no live basket on THIS process yet: pos.Service starts empty, by
// construction, on every process start. So any of its own rows still present
// belong to a process that ended without releasing them (a crash, a kill, a
// power loss) — every one is stale, unconditionally, with no per-row
// judgement call needed. Without this, a table claimed right before an
// unclean shutdown stays occupied forever: nothing else ever revisits an
// orphaned row (the picker filters occupied tables out, /api/pos/table and
// /api/pos/held/table both reject a pick on one) except the manager-
// initiated manual override, ForceReleaseTableClaim (ut-docs#1393).
//
// Scoped to till_id = ” since ut-docs#1703 gave claims an owner (independent
// review, 2026-09-07). The sweep used to be unconditional — every row, whoever
// owned it — which on a PRIMARY also wiped the claims of every REPLICA: tills
// that are still running, with those tables still live on their screens (a
// replica is unaffected either way; every row it writes locally is a ” row).
// A primary restart therefore
// re-opened exactly the cross-till double-claim #1703 exists to close: the
// replica keeps its table locally while the primary now reads it free and
// hands it to the next till that asks. Replica-owned rows need no sweep here
// anyway — ClaimTableForTill's TTL reconciliation is what expires them, and
// only for a till that has genuinely gone quiet.
//
// held_sales is NOT touched here — a parked order surviving a restart is
// the intended offline-first durability held_sales exists for, not a
// leftover to clear.
func (r *POSRepo) ClearLocalTableClaims(ctx context.Context) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM table_claims WHERE till_id = ''`); err != nil {
		return fmt.Errorf("clear local table claims: %w", err)
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
