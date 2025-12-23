package data

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func setupSyncTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	stmts := []string{
		`PRAGMA foreign_keys = ON;`,
		`CREATE TABLE sales (
			id TEXT PRIMARY KEY,
			receipt_no TEXT,
			sync_status TEXT NOT NULL,
			sync_attempts INTEGER NOT NULL DEFAULT 0,
			sync_next_attempt_at TEXT,
			sync_last_error TEXT,
			total INTEGER NOT NULL DEFAULT 0,
			tender_type TEXT NOT NULL DEFAULT 'unknown',
			created_at TEXT NOT NULL
		);`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("setup stmt failed: %v", err)
		}
	}
	return db
}

func seedSyncSale(t *testing.T, db *sql.DB, id, receipt, status, nextAttempt, createdAt string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO sales (id, receipt_no, sync_status, sync_attempts, sync_next_attempt_at, sync_last_error, total, tender_type, created_at) VALUES(?, ?, ?, 0, ?, '', 500, 'cash', ?)`,
		id, receipt, status, nullIfEmpty(nextAttempt), createdAt); err != nil {
		t.Fatalf("seed sale: %v", err)
	}
}

func TestPOSRepo_ListQueuedSales(t *testing.T) {
	db := setupSyncTestDB(t)
	defer db.Close()

	repo := NewPOSRepo(db)
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	ctx := context.Background()

	seedSyncSale(t, db, "sale-1", "R001", "queued", now.Add(-time.Minute).Format(time.RFC3339), now.Add(-time.Minute).Format(time.RFC3339))
	seedSyncSale(t, db, "sale-2", "R002", "queued", now.Add(time.Minute).Format(time.RFC3339), now.Add(time.Minute).Format(time.RFC3339))
	seedSyncSale(t, db, "sale-3", "R003", "synced", now.Add(-time.Minute).Format(time.RFC3339), now.Add(-time.Minute).Format(time.RFC3339))

	rows, err := repo.ListQueuedSales(ctx, 10, now.Format(time.RFC3339))
	if err != nil {
		t.Fatalf("list queued sales: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 queued sale, got %d", len(rows))
	}
	if rows[0].ID != "sale-1" {
		t.Fatalf("expected sale-1, got %s", rows[0].ID)
	}
	if rows[0].SyncAttempts != 0 {
		t.Fatalf("expected sync_attempts 0, got %d", rows[0].SyncAttempts)
	}
}

func TestPOSRepo_BumpSaleSyncAttempt(t *testing.T) {
	db := setupSyncTestDB(t)
	defer db.Close()

	repo := NewPOSRepo(db)
	ctx := context.Background()
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)

	seedSyncSale(t, db, "sale-1", "R001", "queued", "", now.Format(time.RFC3339))

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if err := repo.BumpSaleSyncAttempt(ctx, tx, "sale-1", now.Add(2*time.Minute).Format(time.RFC3339), "timeout"); err != nil {
		_ = tx.Rollback()
		t.Fatalf("bump sync attempt: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit tx: %v", err)
	}

	var attempts int
	var nextAttempt string
	var lastError string
	if err := db.QueryRow(`SELECT sync_attempts, sync_next_attempt_at, sync_last_error FROM sales WHERE id = ?`, "sale-1").Scan(&attempts, &nextAttempt, &lastError); err != nil {
		t.Fatalf("read sale: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("expected sync_attempts 1, got %d", attempts)
	}
	if nextAttempt == "" {
		t.Fatalf("expected next attempt to be set")
	}
	if lastError != "timeout" {
		t.Fatalf("expected last_error timeout, got %s", lastError)
	}
}
