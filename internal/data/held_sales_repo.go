package data

import (
	"context"
	"database/sql"
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
	// UpdatedAt (ADR-0093, ut-docs#1920) is when the row's CONTENTS last
	// changed (same datetime('now') UTC "2006-01-02 15:04:05" shape as
	// CreatedAt, migration 030). Every content-changing write here -- Insert,
	// Upsert, SetTable -- advances it; unlike CreatedAt it is meant to move.
	// It is the cross-till ordering key: UpsertIfNewer (the primary side of
	// the held-sale write-through) applies an incoming row only when its
	// UpdatedAt is >= the stored one, so a stale concurrent write from a
	// second till loses cleanly instead of clobbering a newer one, and the
	// replica-side open-orders merge (internal/pages/open_orders_page.go)
	// picks the greater UpdatedAt when a row exists on both sides. Read by
	// List/Get. Left "" by ordinary callers (the write stamps it); set only
	// when mirroring a row the primary already stamped, so the local copy
	// carries the primary's value rather than a second, skewed clock.
	UpdatedAt string
}

// HeldSalesRepo owns all SQL for the held_sales table.
type HeldSalesRepo struct {
	db *sql.DB
}

func NewHeldSalesRepo(db *sql.DB) *HeldSalesRepo {
	return &HeldSalesRepo{db: db}
}

var heldSalesObs = newRepoObservability("held_sales")

func (r *HeldSalesRepo) Insert(ctx context.Context, h HeldSale) error {
	var err error
	done := heldSalesObs.trace("insert")
	defer func() { done(err) }()
	_, err = r.db.ExecContext(ctx, `
INSERT INTO held_sales (id, label, total_minor, line_count, payload, table_id, updated_at)
VALUES (?, ?, ?, ?, ?, ?, datetime('now'))
`, h.ID, h.Label, h.TotalMinor, h.LineCount, h.Payload, nullIfEmpty(h.TableID))
	if err != nil {
		return heldSalesObs.wrapf("insert", "insert held sale %s", err, h.ID)
	}
	return nil
}

// Upsert (ut-docs#1918) writes a held sale under a caller-chosen, STABLE id:
// a re-park of an order that was resumed from an existing row. Insert-or-
// update on the id, so it is correct whether the resume handler's delete of
// the original row went through (the normal case -- this recreates it) or
// silently failed ("a stale row is the lesser evil" there -- this then
// refreshes it in place instead of tripping the primary key). created_at
// is honoured from h.CreatedAt when set (the original first-parked time
// the caller remembered across the delete) and defaults to now otherwise;
// on the update path it is deliberately left alone, so the row's age
// always counts from the first park.
//
// updated_at (ADR-0093) is the opposite: it moves on BOTH paths. It is
// stamped datetime('now') by default, or taken from h.UpdatedAt when set --
// the same honour-if-set shape created_at uses -- because the one caller
// that sets it is the replica-side mirror of a row the PRIMARY already
// stamped (internal/pages/held_sale_sync_proxy.go): the local copy must
// carry the primary's value, not this till's own clock a network hop later,
// or the open-orders merge would prefer the local copy over a genuinely
// newer primary write that landed inside that hop.
func (r *HeldSalesRepo) Upsert(ctx context.Context, h HeldSale) error {
	var err error
	done := heldSalesObs.trace("upsert")
	defer func() { done(err) }()
	_, err = r.db.ExecContext(ctx, `
INSERT INTO held_sales (id, label, total_minor, line_count, payload, table_id, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, COALESCE(NULLIF(?, ''), datetime('now')), COALESCE(NULLIF(?, ''), datetime('now')))
ON CONFLICT(id) DO UPDATE SET
	label = excluded.label,
	total_minor = excluded.total_minor,
	line_count = excluded.line_count,
	payload = excluded.payload,
	table_id = excluded.table_id,
	updated_at = excluded.updated_at
`, h.ID, h.Label, h.TotalMinor, h.LineCount, h.Payload, nullIfEmpty(h.TableID), h.CreatedAt, h.UpdatedAt)
	if err != nil {
		return heldSalesObs.wrapf("upsert", "upsert held sale %s", err, h.ID)
	}
	return nil
}

// UpsertIfNewer (ADR-0093 Decision 2, ut-docs#1920) is the PRIMARY-side
// write behind POST /api/sync/held-sales/upsert: Upsert, guarded so an
// incoming row is applied only when its updated_at is >= the stored one (or
// the row does not exist yet). Two tills racing to update the same parked
// order serialize on this database's single writer, and the one carrying
// the older stamp is refused -- applied=false, NOT an error, exactly the
// way ClaimTableForTill answers "someone else has it": a lost race is an
// ordinary outcome the caller reconciles (by reading the row back), not a
// fault. Same guarded-UPDATE shape ADR-0084's `balance >= ?` predicate uses
// for vouchers; SQLite allows a WHERE on DO UPDATE, and RowsAffected is 0
// when that WHERE refuses, which is the whole applied signal.
//
// An empty h.UpdatedAt is stamped datetime('now') on THIS (the primary's)
// clock, and a replica's own fresh writes always arrive empty (see
// heldSaleWriteThrough) -- so every write that goes through the primary is
// ordered by ONE clock, and inter-till clock skew cannot make a replica's
// genuine latest write lose. Only a caller that is deliberately replaying
// a row already stamped elsewhere sends an explicit value. `>=` rather
// than `>` because the stamp has one-second resolution: a re-park within
// the same second as the previous write is still the latest write.
// created_at follows Upsert's rule exactly (honoured on insert, untouched
// on update).
func (r *HeldSalesRepo) UpsertIfNewer(ctx context.Context, h HeldSale) (applied bool, err error) {
	done := heldSalesObs.trace("upsert_if_newer")
	defer func() { done(err) }()
	res, err := r.db.ExecContext(ctx, `
INSERT INTO held_sales (id, label, total_minor, line_count, payload, table_id, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, COALESCE(NULLIF(?, ''), datetime('now')), COALESCE(NULLIF(?, ''), datetime('now')))
ON CONFLICT(id) DO UPDATE SET
	label = excluded.label,
	total_minor = excluded.total_minor,
	line_count = excluded.line_count,
	payload = excluded.payload,
	table_id = excluded.table_id,
	updated_at = excluded.updated_at
WHERE excluded.updated_at >= held_sales.updated_at
`, h.ID, h.Label, h.TotalMinor, h.LineCount, h.Payload, nullIfEmpty(h.TableID), h.CreatedAt, h.UpdatedAt)
	if err != nil {
		return false, heldSalesObs.wrapf("upsert_if_newer", "upsert held sale %s if newer", err, h.ID)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, heldSalesObs.wrapf("upsert_if_newer", "upsert held sale %s if newer: rows affected", err, h.ID)
	}
	return n > 0, nil
}

func (r *HeldSalesRepo) List(ctx context.Context) ([]HeldSale, error) {
	var err error
	done := heldSalesObs.trace("list")
	defer func() { done(err) }()
	rows, err := r.db.QueryContext(ctx, `
SELECT id, label, total_minor, line_count, payload, COALESCE(table_id, ''), created_at, updated_at
FROM held_sales ORDER BY created_at ASC
`)
	if err != nil {
		return nil, heldSalesObs.wrapf("list", "list held sales", err)
	}
	defer rows.Close()
	var out []HeldSale
	for rows.Next() {
		var h HeldSale
		if err = rows.Scan(&h.ID, &h.Label, &h.TotalMinor, &h.LineCount, &h.Payload, &h.TableID, &h.CreatedAt, &h.UpdatedAt); err != nil {
			return nil, heldSalesObs.wrapf("list", "scan held sale", err)
		}
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
	err = r.db.QueryRowContext(ctx, `
SELECT id, label, total_minor, line_count, payload, COALESCE(table_id, ''), created_at, updated_at
FROM held_sales WHERE id = ?
`, id).Scan(&h.ID, &h.Label, &h.TotalMinor, &h.LineCount, &h.Payload, &h.TableID, &h.CreatedAt, &h.UpdatedAt)
	if err == sql.ErrNoRows {
		err = nil
		return HeldSale{}, false, nil
	}
	if err != nil {
		return HeldSale{}, false, heldSalesObs.wrapf("get", "get held sale %s", err, id)
	}
	return h, true, nil
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

// SetTable moves a held (parked) sale onto a different table -- or clears
// its assignment entirely when tableID is "" -- without touching any of
// its other fields (ut-docs#820). A no-op, not an error, when id doesn't
// match any held sale, mirroring Delete's existing convention: the caller
// (the "move order to a different table" handler) has already validated
// the target table is free before calling this, so an unknown id here
// means the held sale was resumed/deleted concurrently, not a bug to
// surface as a hard failure. A table move is a content change under
// ADR-0093's ordering key, so it advances updated_at like any other write.
func (r *HeldSalesRepo) SetTable(ctx context.Context, id, tableID string) error {
	var err error
	done := heldSalesObs.trace("set_table")
	defer func() { done(err) }()
	_, err = r.db.ExecContext(ctx, `UPDATE held_sales SET table_id = ?, updated_at = datetime('now') WHERE id = ?`, nullIfEmpty(tableID), id)
	if err != nil {
		return heldSalesObs.wrapf("set_table", "set table for held sale %s", err, id)
	}
	return nil
}
