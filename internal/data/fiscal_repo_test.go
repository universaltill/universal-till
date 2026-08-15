package data

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// newFiscalTSETestDB creates a minimal in-memory schema with just the table
// RecordFiscalTSESignature/GetFiscalTSESignature need — this package's
// convention of a small hand-rolled schema per test file (see audit_test.go),
// kept column-identical to internal/db/migrations/048_fiscal_tse_signatures.sql.
func newFiscalTSETestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE fiscal_tse_signatures (
	sale_id             TEXT PRIMARY KEY,
	transaction_number  INTEGER NOT NULL DEFAULT 0,
	signature_counter   INTEGER NOT NULL DEFAULT 0,
	serial_number       TEXT NOT NULL DEFAULT '',
	start_time          TEXT NOT NULL DEFAULT '',
	log_time            TEXT NOT NULL DEFAULT '',
	signature           TEXT NOT NULL,
	signature_algorithm TEXT NOT NULL DEFAULT '',
	created_at          TEXT NOT NULL DEFAULT (datetime('now'))
);`); err != nil {
		t.Fatalf("setup stmt failed: %v", err)
	}
	return db
}

// ut-docs#585: a signed sale's §6 KassenSichV evidence (contract
// fiscal-sign-ask.md v1.1.0's `tse` object) round-trips through the
// repository intact.
func TestFiscalTSESignature_RoundTrip(t *testing.T) {
	db := newFiscalTSETestDB(t)
	repo := NewPOSRepo(db)
	ctx := context.Background()

	in := FiscalTSESignature{
		SaleID:             "sale-1",
		TransactionNumber:  4711,
		SignatureCounter:   12345,
		SerialNumber:       "9d5e-serial",
		StartTime:          "2026-08-15T10:31:00Z",
		LogTime:            "2026-08-15T10:31:02Z",
		Signature:          "MEQCIFsig==",
		SignatureAlgorithm: "ecdsa-plain-SHA256",
	}
	if err := repo.RecordFiscalTSESignature(ctx, in); err != nil {
		t.Fatalf("RecordFiscalTSESignature: %v", err)
	}

	got, ok, err := repo.GetFiscalTSESignature(ctx, "sale-1")
	if err != nil {
		t.Fatalf("GetFiscalTSESignature: %v", err)
	}
	if !ok || got == nil {
		t.Fatal("expected the recorded signature to be found")
	}
	if got.CreatedAt == "" {
		t.Fatal("expected CreatedAt stamped by the DB")
	}
	got.CreatedAt = ""
	if *got != in {
		t.Fatalf("round-trip mismatch:\n got %+v\nwant %+v", *got, in)
	}
}

// A missing sale is (nil, false, nil) — not an error: the caller renders no
// TSE block, never placeholders.
func TestFiscalTSESignature_MissingIsNotAnError(t *testing.T) {
	db := newFiscalTSETestDB(t)
	repo := NewPOSRepo(db)

	got, ok, err := repo.GetFiscalTSESignature(context.Background(), "nope")
	if err != nil {
		t.Fatalf("expected no error for a missing sale, got %v", err)
	}
	if ok || got != nil {
		t.Fatalf("expected (nil, false) for a missing sale, got (%+v, %v)", got, ok)
	}
}

// Idempotency: recording the same sale twice (e.g. a duplicated tender
// retry) neither errors nor double-inserts nor overwrites the first
// recorded evidence — the first write wins, matching the contract's
// "a re-sign of the same sale never duplicates or overwrites".
func TestFiscalTSESignature_RecordIsIdempotent(t *testing.T) {
	db := newFiscalTSETestDB(t)
	repo := NewPOSRepo(db)
	ctx := context.Background()

	first := FiscalTSESignature{SaleID: "sale-1", TransactionNumber: 1, Signature: "sig-a"}
	second := FiscalTSESignature{SaleID: "sale-1", TransactionNumber: 2, Signature: "sig-b"}
	if err := repo.RecordFiscalTSESignature(ctx, first); err != nil {
		t.Fatalf("first record: %v", err)
	}
	if err := repo.RecordFiscalTSESignature(ctx, second); err != nil {
		t.Fatalf("second record must not error: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM fiscal_tse_signatures WHERE sale_id='sale-1'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected exactly one row, got %d", n)
	}
	got, ok, err := repo.GetFiscalTSESignature(ctx, "sale-1")
	if err != nil || !ok {
		t.Fatalf("get after double record: ok=%v err=%v", ok, err)
	}
	if got.Signature != "sig-a" || got.TransactionNumber != 1 {
		t.Fatalf("first recorded evidence must win, got %+v", got)
	}
}
