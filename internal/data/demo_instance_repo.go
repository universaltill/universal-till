package data

import (
	"context"
	"database/sql"
	"errors"
)

// DemoInstanceRepo reads and writes the demo-template flag (migration 046,
// ADR-0113 §1.2, ut-docs#2687): the single demo_instance row that marks a
// database as a public-demo template. internal/app's start gate is the
// reader; the demo template build (and tests) are the only writers.
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

// MarkDemoInstance sets the demo flag. Idempotent. Never called by a
// running till: only the demo template build and tests.
func (r *DemoInstanceRepo) MarkDemoInstance(ctx context.Context) error {
	_, err := r.db.ExecContext(ctx, `INSERT OR IGNORE INTO demo_instance (id) VALUES (1)`)
	return err
}
