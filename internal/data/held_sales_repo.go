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
	CreatedAt  string
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
INSERT INTO held_sales (id, label, total_minor, line_count, payload) VALUES (?, ?, ?, ?, ?)
`, h.ID, h.Label, h.TotalMinor, h.LineCount, h.Payload)
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
SELECT id, label, total_minor, line_count, payload, created_at
FROM held_sales ORDER BY created_at ASC
`)
	if err != nil {
		return nil, heldSalesObs.wrapf("list", "list held sales", err)
	}
	defer rows.Close()
	var out []HeldSale
	for rows.Next() {
		var h HeldSale
		if err = rows.Scan(&h.ID, &h.Label, &h.TotalMinor, &h.LineCount, &h.Payload, &h.CreatedAt); err != nil {
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
SELECT id, label, total_minor, line_count, payload, created_at
FROM held_sales WHERE id = ?
`, id).Scan(&h.ID, &h.Label, &h.TotalMinor, &h.LineCount, &h.Payload, &h.CreatedAt)
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
