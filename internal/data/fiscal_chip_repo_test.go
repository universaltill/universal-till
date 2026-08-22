package data

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// newFiscalChipTestDB is a minimal hand-rolled schema with just what
// LatestSaleID/CountUnresolvedAuditActionsSince need — same convention as
// newAuditTestDB (audit_test.go), plus a bare sales table.
func newFiscalChipTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	stmts := []string{
		`CREATE TABLE sales (id TEXT PRIMARY KEY, created_at TEXT NOT NULL);`,
		`CREATE TABLE audit_log (id TEXT PRIMARY KEY, actor_id TEXT, entity_type TEXT NOT NULL, entity_id TEXT NOT NULL, action TEXT NOT NULL, data_json TEXT, created_at TEXT NOT NULL DEFAULT (datetime('now')), blocked_actor_id TEXT);`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("setup stmt failed: %v", err)
		}
	}
	return db
}

func TestPOSRepo_LatestSaleID(t *testing.T) {
	db := newFiscalChipTestDB(t)
	repo := NewPOSRepo(db)
	ctx := context.Background()

	// No sales at all.
	if _, ok, err := repo.LatestSaleID(ctx); err != nil {
		t.Fatalf("LatestSaleID on empty table: %v", err)
	} else if ok {
		t.Fatal("expected ok=false with no sales")
	}

	if _, err := db.Exec(`INSERT INTO sales(id, created_at) VALUES ('s1','2026-08-20T09:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sales(id, created_at) VALUES ('s2','2026-08-20T10:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sales(id, created_at) VALUES ('s3','2026-08-20T08:00:00Z')`); err != nil {
		t.Fatal(err)
	}

	id, ok, err := repo.LatestSaleID(ctx)
	if err != nil {
		t.Fatalf("LatestSaleID: %v", err)
	}
	if !ok || id != "s2" {
		t.Fatalf("expected the most recently created sale s2, got id=%q ok=%v", id, ok)
	}
}

func TestPOSRepo_CountUnresolvedAuditActionsSince(t *testing.T) {
	db := newFiscalChipTestDB(t)
	repo := NewPOSRepo(db)
	ctx := context.Background()

	actions := []string{"unsigned_fiscal_signing", "unsigned_fiscal_cannot_sign"}
	since := mustParseRFC3339(t, "2026-08-20T00:00:00Z")

	// Nothing yet.
	n, err := repo.CountUnresolvedAuditActionsSince(ctx, "sale", actions, since)
	if err != nil {
		t.Fatalf("count on empty table: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0, got %d", n)
	}

	// A gap before the window: excluded.
	if err := repo.InsertAudit(ctx, nil, "", "sale", "old1", "unsigned_fiscal_signing", nil, "2026-08-19T23:00:00Z", ""); err != nil {
		t.Fatal(err)
	}
	// Two distinct gapped sales inside the window.
	if err := repo.InsertAudit(ctx, nil, "", "sale", "s1", "unsigned_fiscal_signing", nil, "2026-08-20T09:00:00Z", ""); err != nil {
		t.Fatal(err)
	}
	if err := repo.InsertAudit(ctx, nil, "", "sale", "s2", "unsigned_fiscal_cannot_sign", nil, "2026-08-20T10:00:00Z", ""); err != nil {
		t.Fatal(err)
	}
	// A resolved gap (historical pre-ADR-0056 shape): must not count.
	if err := repo.InsertAudit(ctx, nil, "", "sale", "s3", "unsigned_fiscal_signing", nil, "2026-08-20T11:00:00Z", ""); err != nil {
		t.Fatal(err)
	}
	if err := repo.InsertAudit(ctx, nil, "", "sale", "s3", "fiscal_signing_resolved", nil, "2026-08-20T11:05:00Z", ""); err != nil {
		t.Fatal(err)
	}
	// A different entity_type with the same action: must not count.
	if err := repo.InsertAudit(ctx, nil, "", "plugin", "s4", "unsigned_fiscal_signing", nil, "2026-08-20T12:00:00Z", ""); err != nil {
		t.Fatal(err)
	}
	// A sale action not in the requested list: must not count.
	if err := repo.InsertAudit(ctx, nil, "", "sale", "s5", "unsigned_override", nil, "2026-08-20T13:00:00Z", ""); err != nil {
		t.Fatal(err)
	}

	n, err = repo.CountUnresolvedAuditActionsSince(ctx, "sale", actions, since)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 unresolved gapped sales in-window, got %d", n)
	}
}

func mustParseRFC3339(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return tm
}
