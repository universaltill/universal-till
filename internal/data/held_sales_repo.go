package data

import (
	"context"
	"database/sql"
	"strings"
)

// HeldSale is a parked in-progress sale (basket snapshot) waiting to be resumed.
type HeldSale struct {
	ID         string
	Label      string
	TotalMinor int64
	LineCount  int
	Payload    string
	// TableID (ut-docs#820, ADR-0054) is the dining table this parked
	// order is assigned to, or "" when none. The column itself predates
	// this field (migration 054_tables.sql, forward-compat for this card).
	TableID string
	// CreatedAt is when the order was FIRST parked (schema default
	// datetime('now'), UTC "2006-01-02 15:04:05"). Read by List/Get; Insert
	// leaves it to the default. Upsert (ut-docs#1918) writes it explicitly
	// when set, so a re-park that recreates a row the resume handler
	// deleted keeps the original first-parked time -- the "age" the Open
	// orders page shows must not reset every time an order is touched.
	CreatedAt string
	// UpdatedAt (ADR-0093, migration 030) is when the row was LAST written,
	// same UTC "2006-01-02 15:04:05" text shape as CreatedAt. Insert and
	// Upsert stamp it with now on every write (both branches), so a local
	// park/re-park always moves it; UpsertIfNewer stores the CALLER's value
	// and is the one write that compares against it -- the predicate the
	// primary-side write-through serializes concurrent tills on. Kept
	// separate from CreatedAt so the Open orders page's "age" (first park)
	// is untouched by this column's existence.
	UpdatedAt string
	// PrimarySynced (ADR-0093 Amendment A, migration 030) is true once THIS
	// till has confirmed the row exists on the primary: the replica
	// write-through sets it when it mirrors a primary-applied write, and
	// ReconcileWithPrimary sets it for every local id a successful primary
	// list returned. A row taken purely through the local-only fallback
	// (primary unreachable) stays false. Sticky: once confirmed, a later
	// local-only write to the same row does not clear it -- the primary
	// still knows the id, so its later absence from the primary's list
	// still means "resolved there" (drop), never "never synced" (keep).
	// Local-only meaning; it never rides the sync wire, and on the primary
	// itself or a standalone till it is simply always false.
	PrimarySynced bool
}

// HeldSalesRepo owns all SQL for the held_sales table.
type HeldSalesRepo struct {
	db *sql.DB
}

func NewHeldSalesRepo(db *sql.DB) *HeldSalesRepo {
	return &HeldSalesRepo{db: db}
}

var heldSalesObs = newRepoObservability("held_sales")

// Upsert (ut-docs#1918) writes a held sale under a caller-chosen, STABLE id:
// a re-park of an order that was resumed from an existing row. Insert-or-
// update on the id, so it is correct whether the resume handler's delete of
// the original row went through (the normal case -- this recreates it) or
// silently failed ("a stale row is the lesser evil" there -- this then
// refreshes it in place instead of tripping the primary key). created_at
// is honoured from h.CreatedAt when set (the original first-parked time
// the caller remembered across the delete) and defaults to now otherwise;
// on the update path it is deliberately left alone, so the row's age
// always counts from the first park. updated_at (ADR-0093) is stamped
// with now on BOTH branches -- this is the unguarded, local-only write
// (a standalone till, the primary's own basket, or a replica's offline
// fallback); h.UpdatedAt is ignored here. The guarded, caller-timestamped
// form is UpsertIfNewer. primary_synced (Amendment A) is written from
// h.PrimarySynced on insert and only ever raised on update
// (MAX(current, incoming)): a local-only fallback write over a row this
// till already confirmed on the primary must not demote it to
// "never synced" -- see HeldSale.PrimarySynced.
func (r *HeldSalesRepo) Upsert(ctx context.Context, h HeldSale) error {
	var err error
	done := heldSalesObs.trace("upsert")
	defer func() { done(err) }()
	_, err = r.db.ExecContext(ctx, `
INSERT INTO held_sales (id, label, total_minor, line_count, payload, table_id, created_at, updated_at, primary_synced)
VALUES (?, ?, ?, ?, ?, ?, COALESCE(NULLIF(?, ''), datetime('now')), datetime('now'), ?)
ON CONFLICT(id) DO UPDATE SET
	label = excluded.label,
	total_minor = excluded.total_minor,
	line_count = excluded.line_count,
	payload = excluded.payload,
	table_id = excluded.table_id,
	updated_at = datetime('now'),
	primary_synced = MAX(held_sales.primary_synced, excluded.primary_synced)
`, h.ID, h.Label, h.TotalMinor, h.LineCount, h.Payload, nullIfEmpty(h.TableID), h.CreatedAt, boolToInt(h.PrimarySynced))
	if err != nil {
		return heldSalesObs.wrapf("upsert", "upsert held sale %s", err, h.ID)
	}
	return nil
}

// UpsertIfNewer (ADR-0093, ut-docs#1920) is the predicate-guarded write
// behind the primary's POST /api/sync/held-sales/upsert: insert-or-update
// on the id, but the update branch only applies when the incoming
// h.UpdatedAt is at or after the row's current updated_at --
//
//	ON CONFLICT(id) DO UPDATE ... WHERE held_sales.updated_at <= excluded.updated_at
//
// Same shape as DebitVoucherForRedemption's `balance >= ?` guard
// (voucher_repo.go): the predicate lives in the statement itself, so two
// near-simultaneous upserts for one id serialize under SQLite's single
// writer and whichever carries the OLDER updated_at simply matches zero
// rows. That is reported as applied=false with a nil error -- a clean,
// detectable refusal the caller can act on, never a silent clobber of a
// newer edit and never mislabelled as a DB fault (RowsAffected's own
// error IS still an error, same reasoning as the voucher guard's review
// finding F8). An equal timestamp applies (an idempotent retry of the
// same write must succeed). h.UpdatedAt is stored as given (it is the
// value later writers are compared against); when empty it falls back
// to now, never the ” placeholder migration 030 lands with, which every
// later write would trivially beat. created_at is honoured on insert and
// left alone on update, exactly as Upsert does (ut-docs#1918); so is
// primary_synced (Amendment A: written on insert, only ever raised on an
// applied update -- the replica's mirror path passes PrimarySynced=true,
// the primary's own endpoint passes false and its rows stay 0).
func (r *HeldSalesRepo) UpsertIfNewer(ctx context.Context, h HeldSale) (applied bool, err error) {
	done := heldSalesObs.trace("upsert_if_newer")
	defer func() { done(err) }()
	res, err := r.db.ExecContext(ctx, `
INSERT INTO held_sales (id, label, total_minor, line_count, payload, table_id, created_at, updated_at, primary_synced)
VALUES (?, ?, ?, ?, ?, ?, COALESCE(NULLIF(?, ''), datetime('now')), COALESCE(NULLIF(?, ''), datetime('now')), ?)
ON CONFLICT(id) DO UPDATE SET
	label = excluded.label,
	total_minor = excluded.total_minor,
	line_count = excluded.line_count,
	payload = excluded.payload,
	table_id = excluded.table_id,
	updated_at = excluded.updated_at,
	primary_synced = MAX(held_sales.primary_synced, excluded.primary_synced)
WHERE held_sales.updated_at <= excluded.updated_at
`, h.ID, h.Label, h.TotalMinor, h.LineCount, h.Payload, nullIfEmpty(h.TableID), h.CreatedAt, h.UpdatedAt, boolToInt(h.PrimarySynced))
	if err != nil {
		return false, heldSalesObs.wrapf("upsert_if_newer", "upsert held sale %s", err, h.ID)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, heldSalesObs.wrapf("upsert_if_newer", "upsert held sale %s: rows affected", err, h.ID)
	}
	return n == 1, nil
}

func (r *HeldSalesRepo) List(ctx context.Context) ([]HeldSale, error) {
	var err error
	done := heldSalesObs.trace("list")
	defer func() { done(err) }()
	rows, err := r.db.QueryContext(ctx, `
SELECT id, label, total_minor, line_count, payload, COALESCE(table_id, ''), created_at, updated_at, primary_synced
FROM held_sales ORDER BY created_at ASC
`)
	if err != nil {
		return nil, heldSalesObs.wrapf("list", "list held sales", err)
	}
	defer rows.Close()
	var out []HeldSale
	for rows.Next() {
		var h HeldSale
		var synced int
		if err = rows.Scan(&h.ID, &h.Label, &h.TotalMinor, &h.LineCount, &h.Payload, &h.TableID, &h.CreatedAt, &h.UpdatedAt, &synced); err != nil {
			return nil, heldSalesObs.wrapf("list", "scan held sale", err)
		}
		h.PrimarySynced = synced != 0
		out = append(out, h)
	}
	if err = rows.Err(); err != nil {
		return nil, heldSalesObs.wrapf("list", "iterate held sales", err)
	}
	return out, nil
}

func (r *HeldSalesRepo) Get(ctx context.Context, id string) (HeldSale, bool, error) {
	var err error
	done := heldSalesObs.trace("get")
	defer func() { done(err) }()
	var h HeldSale
	var synced int
	err = r.db.QueryRowContext(ctx, `
SELECT id, label, total_minor, line_count, payload, COALESCE(table_id, ''), created_at, updated_at, primary_synced
FROM held_sales WHERE id = ?
`, id).Scan(&h.ID, &h.Label, &h.TotalMinor, &h.LineCount, &h.Payload, &h.TableID, &h.CreatedAt, &h.UpdatedAt, &synced)
	if err == sql.ErrNoRows {
		err = nil
		return HeldSale{}, false, nil
	}
	if err != nil {
		return HeldSale{}, false, heldSalesObs.wrapf("get", "get held sale %s", err, id)
	}
	h.PrimarySynced = synced != 0
	return h, true, nil
}

// ReconcileWithPrimary (ADR-0093 Amendment A) is the replica's local
// clean-up after ONE successful fetch of the primary's full open-orders
// list, whose ids are presentIDs. In a single transaction:
//
//   - every local row whose id the primary DID return is marked
//     primary_synced = 1 (this till has now confirmed it exists there);
//   - every local row with primary_synced = 1 whose id the primary did NOT
//     return is deleted -- the primary once had it and has since resolved
//     it (resumed / cashed out / abandoned on another till), so the local
//     mirror is a ghost that could otherwise be re-rung after the money
//     already moved (review finding F2);
//   - a local row with primary_synced = 0 absent from the list is left
//     exactly alone: the primary never learned of it (parked while it was
//     unreachable), the legitimate outage-taken order the merge keeps.
//
// Only ever called with the list of a fetch that actually succeeded -- a
// failed / partial fetch must never reach here, or every mirror would be
// dropped as "resolved". Returns how many ghosts were dropped, for the
// caller's log line. Nothing here touches the primary.
func (r *HeldSalesRepo) ReconcileWithPrimary(ctx context.Context, presentIDs []string) (dropped int64, err error) {
	done := heldSalesObs.trace("reconcile_with_primary")
	defer func() { done(err) }()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, heldSalesObs.wrapf("reconcile_with_primary", "begin", err)
	}
	defer tx.Rollback()
	args := make([]any, 0, len(presentIDs))
	for _, id := range presentIDs {
		args = append(args, id)
	}
	inList := "(" + strings.TrimSuffix(strings.Repeat("?,", len(presentIDs)), ",") + ")"
	if len(presentIDs) > 0 {
		if _, err = tx.ExecContext(ctx, `UPDATE held_sales SET primary_synced = 1 WHERE id IN `+inList, args...); err != nil {
			return 0, heldSalesObs.wrapf("reconcile_with_primary", "mark synced", err)
		}
	}
	del := `DELETE FROM held_sales WHERE primary_synced = 1`
	if len(presentIDs) > 0 {
		del += ` AND id NOT IN ` + inList
	}
	res, err := tx.ExecContext(ctx, del, args...)
	if err != nil {
		return 0, heldSalesObs.wrapf("reconcile_with_primary", "drop resolved mirrors", err)
	}
	if dropped, err = res.RowsAffected(); err != nil {
		return 0, heldSalesObs.wrapf("reconcile_with_primary", "rows affected", err)
	}
	if err = tx.Commit(); err != nil {
		return 0, heldSalesObs.wrapf("reconcile_with_primary", "commit", err)
	}
	return dropped, nil
}

// heldSaleTombstoneTTL is how long a primary-side tombstone keeps answering
// "resolved" (ADR-0093 Amendment B, ut-docs#2712): long enough to outlast
// any realistic lost-reply window, short enough that a till offline for
// days finds nothing and trusts its own local row (Decision 4's
// offline-first guarantee). SQLite datetime() modifier text.
const heldSaleTombstoneTTL = "-24 hours"

// tombstoneHeldSaleTx writes/refreshes id's tombstone -- stamped with the
// till that resolved it (see ClaimAndTombstone) -- and prunes every
// tombstone past heldSaleTombstoneTTL, inside the caller's transaction --
// the shared tail of both primary-side deletions, so neither can delete a
// row without leaving the proof behind.
func tombstoneHeldSaleTx(ctx context.Context, tx *sql.Tx, id, till string) error {
	if _, err := tx.ExecContext(ctx, `
INSERT INTO held_sales_tombstones (id, deleted_at, till) VALUES (?, datetime('now'), ?)
ON CONFLICT(id) DO UPDATE SET deleted_at = excluded.deleted_at, till = excluded.till
`, id, till); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM held_sales_tombstones WHERE deleted_at < datetime('now', ?)`, heldSaleTombstoneTTL)
	return err
}

// ClaimAndTombstone (ADR-0093 Amendment B, ut-docs#2712) is the primary's
// atomic resume hand-off behind POST /api/sync/held-sales/claim (and the
// primary till's own resume). One transaction: DELETE ... RETURNING takes
// the row and makes this a write transaction from its first statement, so
// two tills claiming the same id serialize on SQLite's single writer and
// exactly one ever gets a row back -- the fix for two tills both restoring
// the same order. A taken row leaves a tombstone (claimed=true, known=true).
// An absent row answers known = "a fresh tombstone written by ANOTHER till
// exists": true means resolved elsewhere (an earlier claim/delete from some
// other till, e.g. after this caller's own push reply was lost), false means
// nothing on the primary contradicts the caller's own copy -- the one
// answer under which a caller may still trust it. Stale tombstones are
// pruned on both branches.
//
// till is the caller's identity (the enrolled replica's tills.id; "" for
// the primary's own resume), stamped on the tombstone it writes and
// compared against the one it finds (independent review of #2712): a
// caller's OWN tombstone never answers known. After a resume the same
// order is re-parked under the same id (ut-docs#1918); when that re-park
// alone fell back to local-only, the re-parking till holds the newest copy
// in the shop, and its own earlier claim's tombstone refusing it would drop
// a genuinely open order -- the offline-first regression this rule closes.
// Its own copy can only be stale when the row was resolved by someone else
// since (then the tombstone carries THEIR till), or when a primary_synced
// mirror outlived the resume (Amendment A F2 still drops that one).
func (r *HeldSalesRepo) ClaimAndTombstone(ctx context.Context, id, till string) (h HeldSale, claimed, known bool, err error) {
	done := heldSalesObs.trace("claim_and_tombstone")
	defer func() { done(err) }()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return HeldSale{}, false, false, heldSalesObs.wrapf("claim_and_tombstone", "begin", err)
	}
	defer tx.Rollback()
	var synced int
	err = tx.QueryRowContext(ctx, `
DELETE FROM held_sales WHERE id = ?
RETURNING id, label, total_minor, line_count, payload, COALESCE(table_id, ''), created_at, updated_at, primary_synced
`, id).Scan(&h.ID, &h.Label, &h.TotalMinor, &h.LineCount, &h.Payload, &h.TableID, &h.CreatedAt, &h.UpdatedAt, &synced)
	switch {
	case err == nil:
		h.PrimarySynced = synced != 0
		claimed, known = true, true
		if err = tombstoneHeldSaleTx(ctx, tx, id, till); err != nil {
			return HeldSale{}, false, false, heldSalesObs.wrapf("claim_and_tombstone", "tombstone held sale %s", err, id)
		}
	case err == sql.ErrNoRows:
		h = HeldSale{}
		if _, err = tx.ExecContext(ctx, `DELETE FROM held_sales_tombstones WHERE deleted_at < datetime('now', ?)`, heldSaleTombstoneTTL); err != nil {
			return HeldSale{}, false, false, heldSalesObs.wrapf("claim_and_tombstone", "prune tombstones", err)
		}
		var resolvedBy string
		switch err = tx.QueryRowContext(ctx, `SELECT till FROM held_sales_tombstones WHERE id = ?`, id).Scan(&resolvedBy); {
		case err == nil:
			known = resolvedBy != till
		case err == sql.ErrNoRows:
			err = nil
		default:
			return HeldSale{}, false, false, heldSalesObs.wrapf("claim_and_tombstone", "read tombstone %s", err, id)
		}
	default:
		return HeldSale{}, false, false, heldSalesObs.wrapf("claim_and_tombstone", "claim held sale %s", err, id)
	}
	if err = tx.Commit(); err != nil {
		return HeldSale{}, false, false, heldSalesObs.wrapf("claim_and_tombstone", "commit", err)
	}
	return h, claimed, known, nil
}

// DeleteAndTombstone (ADR-0093 Amendment B) is the plain primary-side
// delete behind POST /api/sync/held-sales/delete: the row goes and its
// tombstone is written in the same transaction, so a stale replica copy of
// an order resolved this way (e.g. by a till still on the pre-claim version
// during a rollout) is refused by a later claim exactly like a claimed one.
// Idempotent -- a missing row still (re)writes the tombstone. till is the
// deleting replica's tills.id, stamped on the tombstone exactly as
// ClaimAndTombstone does. Delete stays the local-only form for a replica's
// own mirror cleanup.
func (r *HeldSalesRepo) DeleteAndTombstone(ctx context.Context, id, till string) (err error) {
	done := heldSalesObs.trace("delete_and_tombstone")
	defer func() { done(err) }()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return heldSalesObs.wrapf("delete_and_tombstone", "begin", err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM held_sales WHERE id = ?`, id); err != nil {
		return heldSalesObs.wrapf("delete_and_tombstone", "delete held sale %s", err, id)
	}
	if err = tombstoneHeldSaleTx(ctx, tx, id, till); err != nil {
		return heldSalesObs.wrapf("delete_and_tombstone", "tombstone held sale %s", err, id)
	}
	if err = tx.Commit(); err != nil {
		return heldSalesObs.wrapf("delete_and_tombstone", "commit", err)
	}
	return nil
}

func (r *HeldSalesRepo) Delete(ctx context.Context, id string) error {
	var err error
	done := heldSalesObs.trace("delete")
	defer func() { done(err) }()
	_, err = r.db.ExecContext(ctx, `DELETE FROM held_sales WHERE id = ?`, id)
	if err != nil {
		return heldSalesObs.wrapf("delete", "delete held sale %s", err, id)
	}
	return nil
}
