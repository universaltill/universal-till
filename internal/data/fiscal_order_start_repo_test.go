package data

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// newFiscalOrderStartTestDB creates the one table RecordFiscalOrderStart /
// GetFiscalOrderStart need — column-identical to
// internal/db/migrations/059_fiscal_order_starts.sql (ADR-0138 D3,
// ut-docs#3310), this package's hand-rolled-schema-per-file convention.
func newFiscalOrderStartTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE fiscal_order_starts (
    order_id     TEXT PRIMARY KEY,
    order_kind   TEXT NOT NULL,
    tx_id        TEXT NOT NULL,
    tx_revision  INTEGER NOT NULL DEFAULT 0,
    created_at   TEXT NOT NULL DEFAULT (datetime('now'))
);`); err != nil {
		t.Fatalf("setup stmt failed: %v", err)
	}
	return db
}

// A captured fiscal.order.start identifier round-trips, keyed on the order's
// own id (held sale id / counter order id), with its kind.
func TestFiscalOrderStart_RecordThenGet(t *testing.T) {
	repo := NewPOSRepo(newFiscalOrderStartTestDB(t))
	ctx := context.Background()
	if err := repo.RecordFiscalOrderStart(ctx, "hold-1", "held", "tx-order-1", 2); err != nil {
		t.Fatalf("record: %v", err)
	}
	got, ok, err := repo.GetFiscalOrderStart(ctx, "hold-1")
	if err != nil || !ok {
		t.Fatalf("expected a row, ok=%v err=%v", ok, err)
	}
	if got.OrderID != "hold-1" || got.OrderKind != "held" || got.TxID != "tx-order-1" || got.TxRevision != 2 || got.CreatedAt == "" {
		t.Fatalf("unexpected row: %+v", *got)
	}
}

// First write wins (ADR-0138 D2/D3 "never silently overwritten"): a second
// record for the same order id never errors and never replaces the original
// capture identifier.
func TestFiscalOrderStart_FirstWriteWins(t *testing.T) {
	repo := NewPOSRepo(newFiscalOrderStartTestDB(t))
	ctx := context.Background()
	if err := repo.RecordFiscalOrderStart(ctx, "hold-1", "held", "tx-first", 1); err != nil {
		t.Fatalf("record first: %v", err)
	}
	if err := repo.RecordFiscalOrderStart(ctx, "hold-1", "counter", "tx-second", 9); err != nil {
		t.Fatalf("a duplicate record must not error: %v", err)
	}
	got, ok, err := repo.GetFiscalOrderStart(ctx, "hold-1")
	if err != nil || !ok {
		t.Fatalf("expected a row, ok=%v err=%v", ok, err)
	}
	if got.TxID != "tx-first" || got.TxRevision != 1 || got.OrderKind != "held" {
		t.Fatalf("first captured identifier must survive a duplicate record, got %+v", *got)
	}
}

// No row is the honest degraded case, not an error.
func TestFiscalOrderStart_MissIsNotAnError(t *testing.T) {
	repo := NewPOSRepo(newFiscalOrderStartTestDB(t))
	got, ok, err := repo.GetFiscalOrderStart(context.Background(), "hold-none")
	if err != nil || ok || got != nil {
		t.Fatalf("expected (nil, false, nil) for a missing row, got (%v, %v, %v)", got, ok, err)
	}
}
