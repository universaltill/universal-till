package data

import (
	"context"
	"database/sql"
	"errors"
)

// DemoInstanceRepo reads and writes the demo-template flag (migration 046,
// ADR-0113 §1.2, ut-docs#2687): the single demo_instance row that marks a
// database as a public-demo template. internal/app's start gate is the
// reader. The only writer is the demo template build (ut-docs#2796), which
// adds its own write path; tests seed the row directly.
type DemoInstanceRepo struct {
	db *sql.DB
}

func NewDemoInstanceRepo(db *sql.DB) *DemoInstanceRepo {
	return &DemoInstanceRepo{db: db}
}

// IsDemoInstance reports whether this database carries the demo flag. A
// query error is returned as-is — the start gate fails closed on it.
func (r *DemoInstanceRepo) IsDemoInstance(ctx context.Context) (bool, error) {
	var id int
	err := r.db.QueryRowContext(ctx, `SELECT id FROM demo_instance WHERE id = 1`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
