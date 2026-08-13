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

	before, err := sampleCount(ctx, tx, "items")
	if err != nil {
		return 0, 0, err
	}
	if _, err := tx.ExecContext(ctx, seeddata.DemoIDsSQL); err != nil {
		return 0, 0, fmt.Errorf("load demo id lists: %w", err)
	}
	if _, err := tx.ExecContext(ctx, seeddata.RemoveDemoSQL); err != nil {
		return 0, 0, fmt.Errorf("remove demo catalogue: %w", err)
	}
	after, err := sampleCount(ctx, tx, "items")
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
	return sampleCount(ctx, r.db, "items")
}

// SeedDemoCustomersPromos (re)inserts the 3 demo customers + 3 demo promo
// codes (ut-docs#567), every row flagged is_sample_data = 1. Idempotent
// (INSERT OR IGNORE) and atomic — the opt-in companion to
// SeedDemoCatalogue, run by the same setup-wizard checkbox.
func (r *DemoSeedRepo) SeedDemoCustomersPromos(ctx context.Context) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin demo customers/promos seed: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, seeddata.DemoCustomersPromosSQL); err != nil {
		return fmt.Errorf("seed demo customers/promos: %w", err)
	}
	return tx.Commit()
}

// RemoveDemoCustomersPromos deletes every UNTOUCHED demo customer and promo
// code (plus reports how many it removed and how many it had to keep) using
// the shared seeddata removal script — the exact rule migration 038
// applies on upgrade. See that script's header for why the promotion safety
// rule differs from the customer/item one (no durable redemption link to
// recover from this schema).
func (r *DemoSeedRepo) RemoveDemoCustomersPromos(ctx context.Context) (removed, kept int, err error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("begin demo customers/promos removal: %w", err)
	}
	defer tx.Rollback()

	before, err := sampleCustomerPromoCount(ctx, tx)
	if err != nil {
		return 0, 0, err
	}
	if _, err := tx.ExecContext(ctx, seeddata.DemoCustomersPromosIDsSQL); err != nil {
		return 0, 0, fmt.Errorf("load demo customer/promo id lists: %w", err)
	}
	if _, err := tx.ExecContext(ctx, seeddata.RemoveDemoCustomersPromosSQL); err != nil {
		return 0, 0, fmt.Errorf("remove demo customers/promos: %w", err)
	}
	after, err := sampleCustomerPromoCount(ctx, tx)
	if err != nil {
		return 0, 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return before - after, after, nil
}

// SampleCustomerPromoCount reports how many sample-data customers + promo
// codes are currently present, combined (drives the Settings "sample data
// present" note alongside SampleItemCount).
func (r *DemoSeedRepo) SampleCustomerPromoCount(ctx context.Context) (int, error) {
	return sampleCustomerPromoCount(ctx, r.db)
}

type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func sampleCount(ctx context.Context, q queryRower, table string) (int, error) {
	var n int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE is_sample_data = 1`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count sample %s: %w", table, err)
	}
	return n, nil
}

func sampleCustomerPromoCount(ctx context.Context, q queryRower) (int, error) {
	customers, err := sampleCount(ctx, q, "customers")
	if err != nil {
		return 0, err
	}
	promos, err := sampleCount(ctx, q, "promotions")
	if err != nil {
		return 0, err
	}
	return customers + promos, nil
}
