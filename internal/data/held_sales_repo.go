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
	TableID   string
	CreatedAt string
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
INSERT INTO held_sales (id, label, total_minor, line_count, payload, table_id) VALUES (?, ?, ?, ?, ?, ?)
`, h.ID, h.Label, h.TotalMinor, h.LineCount, h.Payload, nullIfEmpty(h.TableID))
	if err != nil {
		return heldSalesObs.wrapf("insert", "insert held sale %s", err, h.ID)
	}
	return nil
}

func (r *HeldSalesRepo) List(ctx context.Context) ([]HeldSale, error) {
	var err error
	done := heldSalesObs.trace("list")
	defer func() { done(err) }()
	rows, err := r.db.QueryContext(ctx, `
SELECT id, label, total_minor, line_count, payload, COALESCE(table_id, ''), created_at
FROM held_sales ORDER BY created_at ASC
`)
	if err != nil {
		return nil, heldSalesObs.wrapf("list", "list held sales", err)
	}
	defer rows.Close()
	var out []HeldSale
	for rows.Next() {
		var h HeldSale
		if err = rows.Scan(&h.ID, &h.Label, &h.TotalMinor, &h.LineCount, &h.Payload, &h.TableID, &h.CreatedAt); err != nil {
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
SELECT id, label, total_minor, line_count, payload, COALESCE(table_id, ''), created_at
FROM held_sales WHERE id = ?
`, id).Scan(&h.ID, &h.Label, &h.TotalMinor, &h.LineCount, &h.Payload, &h.TableID, &h.CreatedAt)
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
// surface as a hard failure.
func (r *HeldSalesRepo) SetTable(ctx context.Context, id, tableID string) error {
	var err error
	done := heldSalesObs.trace("set_table")
	defer func() { done(err) }()
	_, err = r.db.ExecContext(ctx, `UPDATE held_sales SET table_id = ? WHERE id = ?`, nullIfEmpty(tableID), id)
	if err != nil {
		return heldSalesObs.wrapf("set_table", "set table for held sale %s", err, id)
	}
	return nil
}
