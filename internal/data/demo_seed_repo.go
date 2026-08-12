package data

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/universaltill/universal-till/internal/data/seeddata"
)

// DemoSeedRepo manages the opt-in demo ("sample data") catalogue —
// ut-docs#539. The catalogue itself lives in internal/data/seeddata, the
// single source of truth shared with migration 036_demo_seed_opt_in.sql.
type DemoSeedRepo struct {
	db *sql.DB
}

func NewDemoSeedRepo(db *sql.DB) *DemoSeedRepo {
	return &DemoSeedRepo{db: db}
}

// SeedDemoCatalogue (re)inserts the demo catalogue, every item flagged
// is_sample_data = 1. Idempotent (INSERT OR IGNORE throughout) and atomic:
// either the whole catalogue lands or none of it does.
func (r *DemoSeedRepo) SeedDemoCatalogue(ctx context.Context) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin demo seed: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, seeddata.DemoCatalogueSQL); err != nil {
		return fmt.Errorf("seed demo catalogue: %w", err)
	}
	return tx.Commit()
}

// RemoveDemoCatalogue deletes every UNTOUCHED demo item (plus its dependents
// and any demo category/brand nothing references any more) and reports how
// many demo items it removed and how many it had to keep because the shop
// already sold them (directly or via a variant) or stock-adjusted them. The
// safety predicate is the shared seeddata removal script — the exact rule
// migration 036 applies on upgrade.
//
// The whole operation runs in one transaction: the TEMP ID tables the shared
// scripts use are per-connection, and a transaction is also what pins
// database/sql to a single connection between the two script executions.
func (r *DemoSeedRepo) RemoveDemoCatalogue(ctx context.Context) (removed, kept int, err error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("begin demo removal: %w", err)
	}
	defer tx.Rollback()

	before, err := sampleItemCount(ctx, tx)
	if err != nil {
		return 0, 0, err
	}
	if _, err := tx.ExecContext(ctx, seeddata.DemoIDsSQL); err != nil {
		return 0, 0, fmt.Errorf("load demo id lists: %w", err)
	}
	if _, err := tx.ExecContext(ctx, seeddata.RemoveDemoSQL); err != nil {
		return 0, 0, fmt.Errorf("remove demo catalogue: %w", err)
	}
	after, err := sampleItemCount(ctx, tx)
	if err != nil {
		return 0, 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return before - after, after, nil
}

// SampleItemCount reports how many sample-data items are currently in the
// catalogue (drives the Settings "sample data present" note).
func (r *DemoSeedRepo) SampleItemCount(ctx context.Context) (int, error) {
	return sampleItemCount(ctx, r.db)
}

type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func sampleItemCount(ctx context.Context, q queryRower) (int, error) {
	var n int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM items WHERE is_sample_data = 1`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count sample items: %w", err)
	}
	return n, nil
}
