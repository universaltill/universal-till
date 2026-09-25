package data_test

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// ADR-0113 §1.2 (ut-docs#2687): the demo flag baked into a demo template
// database. A freshly migrated database — every real till — never carries
// it; MarkDemoInstance sets it, idempotently, and it reads back.
func TestDemoInstanceRepo_FlagRoundTrip(t *testing.T) {
	dbh, err := db.Open(testsupport.MigratedDBFile(t, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { dbh.Close() })
	repo := data.NewDemoInstanceRepo(dbh.DB)
	ctx := context.Background()

	if on, err := repo.IsDemoInstance(ctx); err != nil || on {
		t.Fatalf("fresh database: IsDemoInstance = (%v, %v), want (false, nil)", on, err)
	}
	for i := 0; i < 2; i++ { // second mark must be a no-op, not a constraint error
		if err := repo.MarkDemoInstance(ctx); err != nil {
			t.Fatalf("MarkDemoInstance #%d: %v", i+1, err)
		}
	}
	if on, err := repo.IsDemoInstance(ctx); err != nil || !on {
		t.Fatalf("after mark: IsDemoInstance = (%v, %v), want (true, nil)", on, err)
	}
	var n int
	if err := dbh.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM demo_instance`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("demo_instance rows = %d (err %v), want exactly 1", n, err)
	}
	// The single-row CHECK: a second, different row is refused by the schema.
	if _, err := dbh.DB.ExecContext(ctx, `INSERT INTO demo_instance (id) VALUES (2)`); err == nil {
		t.Fatal("demo_instance accepted a row with id 2; want the CHECK (id = 1) to refuse it")
	}
}
