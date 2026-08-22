package data

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// newFiscalChipTestDB is a minimal hand-rolled schema with just what
// LatestLocalSaleID/CountUnresolvedAuditActionsSince need — same convention as
// newAuditTestDB (audit_test.go), plus a bare sales table.
func newFiscalChipTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	stmts := []string{
		`CREATE TABLE sales (id TEXT PRIMARY KEY, created_at TEXT NOT NULL, till_id TEXT NOT NULL DEFAULT '');`,
		`CREATE TABLE audit_log (id TEXT PRIMARY KEY, actor_id TEXT, entity_type TEXT NOT NULL, entity_id TEXT NOT NULL, action TEXT NOT NULL, data_json TEXT, created_at TEXT NOT NULL DEFAULT (datetime('now')), blocked_actor_id TEXT);`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("setup stmt failed: %v", err)
		}
	}
	return db
}

func TestPOSRepo_LatestLocalSaleID(t *testing.T) {
	db := newFiscalChipTestDB(t)
	repo := NewPOSRepo(db)
	ctx := context.Background()

	// No sales at all.
	if _, ok, err := repo.LatestLocalSaleID(ctx); err != nil {
		t.Fatalf("LatestLocalSaleID on empty table: %v", err)
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

	id, ok, err := repo.LatestLocalSaleID(ctx)
	if err != nil {
		t.Fatalf("LatestLocalSaleID: %v", err)
	}
	if !ok || id != "s2" {
		t.Fatalf("expected the most recently created sale s2, got id=%q ok=%v", id, ok)
	}

	// Independent review, ut-docs#685: a journaled-in REPLICA sale
	// (till_id != '', stamped by SetSaleProvenance with the ORIGIN's
	// created_at, so it can legitimately be the newest row in the table)
	// must never be picked. It never went through completeTender's
	// fiscal.sign.ask hook on this node, so it can never carry a local
	// unsigned_fiscal_signing marker — letting it win would flip the chip
	// green while THIS till's own last sale sits unsigned.
	if _, err := db.Exec(`INSERT INTO sales(id, created_at, till_id) VALUES ('foreign','2026-08-20T23:00:00Z','till-b')`); err != nil {
		t.Fatal(err)
	}
	id, ok, err = repo.LatestLocalSaleID(ctx)
	if err != nil {
		t.Fatalf("LatestLocalSaleID after a journaled-in sale: %v", err)
	}
	if !ok || id != "s2" {
		t.Fatalf("a replica's journaled-in sale must not win: got id=%q ok=%v, want s2", id, ok)
	}

	// Same-second tie: the later-inserted row wins, deterministically, so
	// the chip doesn't flicker between two equally-timestamped sales.
	if _, err := db.Exec(`INSERT INTO sales(id, created_at) VALUES ('tie-a','2026-08-20T12:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sales(id, created_at) VALUES ('tie-b','2026-08-20T12:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		id, ok, err = repo.LatestLocalSaleID(ctx)
		if err != nil || !ok || id != "tie-b" {
			t.Fatalf("same-second tie must resolve stably to the later insert: got id=%q ok=%v err=%v", id, ok, err)
		}
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
