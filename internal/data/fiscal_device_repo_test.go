package data

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// newFiscalDeviceTestDB mirrors newFiscalTSETestDB's convention: the one
// table these methods touch, column-identical to 001_init.sql's
// fiscal_device_receipts.
func newFiscalDeviceTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE fiscal_device_receipts (
    sale_id      TEXT PRIMARY KEY,
    device_kind  TEXT NOT NULL DEFAULT 'okc',
    maker        TEXT NOT NULL DEFAULT '',
    serial       TEXT NOT NULL DEFAULT '',
    receipt_no   TEXT NOT NULL,
    receipt_kind TEXT NOT NULL DEFAULT 'mali_fis',
    z_no         INTEGER NOT NULL DEFAULT 0,
    issued_at    TEXT NOT NULL DEFAULT '',
    created_at   TEXT NOT NULL DEFAULT (datetime('now'))
);`); err != nil {
		t.Fatalf("setup stmt failed: %v", err)
	}
	return db
}

func TestFiscalDeviceReceipt_RoundTrip(t *testing.T) {
	db := newFiscalDeviceTestDB(t)
	repo := NewPOSRepo(db)
	ctx := context.Background()

	in := FiscalDeviceReceipt{
		SaleID:      "sale-1",
		DeviceKind:  "okc",
		Maker:       "beko",
		Serial:      "AV0001234",
		ReceiptNo:   "0000042",
		ReceiptKind: "mali_fis",
		ZNo:         7,
		IssuedAt:    "2026-09-03T10:12:00+03:00",
	}
	if err := repo.RecordFiscalDeviceReceipt(ctx, in); err != nil {
		t.Fatalf("RecordFiscalDeviceReceipt: %v", err)
	}
	got, ok, err := repo.GetFiscalDeviceReceipt(ctx, "sale-1")
	if err != nil || !ok {
		t.Fatalf("GetFiscalDeviceReceipt: ok=%v err=%v", ok, err)
	}
	if got.Maker != in.Maker || got.Serial != in.Serial || got.ReceiptNo != in.ReceiptNo ||
		got.ReceiptKind != in.ReceiptKind || got.ZNo != in.ZNo || got.IssuedAt != in.IssuedAt || got.DeviceKind != "okc" {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	if got.CreatedAt == "" {
		t.Fatal("CreatedAt should be stamped by the DB")
	}
}

// Defaults: an empty device_kind / receipt_kind falls back to the schema's
// own defaults rather than storing "".
func TestFiscalDeviceReceipt_Defaults(t *testing.T) {
	db := newFiscalDeviceTestDB(t)
	repo := NewPOSRepo(db)
	ctx := context.Background()
	if err := repo.RecordFiscalDeviceReceipt(ctx, FiscalDeviceReceipt{SaleID: "s", ReceiptNo: "1"}); err != nil {
		t.Fatal(err)
	}
	got, _, _ := repo.GetFiscalDeviceReceipt(ctx, "s")
	if got.DeviceKind != "okc" || got.ReceiptKind != "mali_fis" {
		t.Fatalf("defaults not applied: %+v", got)
	}
}

// First write wins: the device printed once; a retried record must not
// overwrite the receipt number already on file.
func TestFiscalDeviceReceipt_FirstWriteWins(t *testing.T) {
	db := newFiscalDeviceTestDB(t)
	repo := NewPOSRepo(db)
	ctx := context.Background()
	_ = repo.RecordFiscalDeviceReceipt(ctx, FiscalDeviceReceipt{SaleID: "s", ReceiptNo: "first"})
	_ = repo.RecordFiscalDeviceReceipt(ctx, FiscalDeviceReceipt{SaleID: "s", ReceiptNo: "second"})
	got, _, _ := repo.GetFiscalDeviceReceipt(ctx, "s")
	if got.ReceiptNo != "first" {
		t.Fatalf("receipt_no = %q, want first", got.ReceiptNo)
	}
}

func TestFiscalDeviceReceipt_Missing(t *testing.T) {
	db := newFiscalDeviceTestDB(t)
	repo := NewPOSRepo(db)
	got, ok, err := repo.GetFiscalDeviceReceipt(context.Background(), "nope")
	if err != nil || ok || got != nil {
		t.Fatalf("missing: got=%v ok=%v err=%v", got, ok, err)
	}
	latest, ok, err := repo.LatestFiscalDeviceReceipt(context.Background())
	if err != nil || ok || latest != nil {
		t.Fatalf("latest on empty table: got=%v ok=%v err=%v", latest, ok, err)
	}
}

func TestFiscalDeviceReceipt_LatestAndCount(t *testing.T) {
	db := newFiscalDeviceTestDB(t)
	repo := NewPOSRepo(db)
	ctx := context.Background()
	// Explicit created_at values so ordering does not depend on the clock.
	for _, row := range []struct{ id, no, at string }{
		{"a", "10", "2026-09-01 08:00:00"},
		{"b", "11", "2026-09-03 09:00:00"},
		{"c", "12", "2026-09-03 09:30:00"},
	} {
		if _, err := db.Exec(`INSERT INTO fiscal_device_receipts (sale_id, receipt_no, created_at) VALUES (?, ?, ?)`, row.id, row.no, row.at); err != nil {
			t.Fatal(err)
		}
	}
	latest, ok, err := repo.LatestFiscalDeviceReceipt(ctx)
	if err != nil || !ok || latest.SaleID != "c" {
		t.Fatalf("latest = %+v ok=%v err=%v", latest, ok, err)
	}
	since := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	n, err := repo.CountFiscalDeviceReceiptsSince(ctx, since)
	if err != nil || n != 2 {
		t.Fatalf("count since 2026-09-03 = %d err=%v, want 2", n, err)
	}
}
