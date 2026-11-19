package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/db"
)

// ut-docs#5068: the marker file triggers one jump, an audit row, and is then
// cleared; without the file nothing happens.
func TestJumpNumbersAfterRestore_MarkerFileDrivesJumpAuditAndClear(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "unitill-pos.db")
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()
	log := &recordingLog{}
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

	jumpNumbersAfterRestore(ctx, d.DB, dbPath, now, log) // no marker: no-op
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'restore_numbers_jumped'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("audit rows without marker = %d, %v", n, err)
	}

	if err := os.WriteFile(filepath.Join(filepath.Dir(dbPath), "restore-numbers-pending"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !db.NumberJumpPending(dbPath) {
		t.Fatal("marker not seen")
	}
	jumpNumbersAfterRestore(ctx, d.DB, dbPath, now, log)
	if len(log.errors) != 0 {
		t.Fatalf("errors: %v", log.errors)
	}
	if db.NumberJumpPending(dbPath) {
		t.Fatal("marker file must be cleared after a recorded jump")
	}
	var entity, id, actor string
	if err := d.DB.QueryRow(`SELECT entity_type, entity_id, actor_id FROM audit_log WHERE action = 'restore_numbers_jumped'`).Scan(&entity, &id, &actor); err != nil {
		t.Fatalf("audit row: %v", err)
	}
	if entity != "backup" || id != "restore" || actor != "system" {
		t.Fatalf("audit = %s/%s/%s", entity, id, actor)
	}
	// A leftover marker file (crash before removal) with the same token must
	// not jump again.
	if err := os.WriteFile(filepath.Join(filepath.Dir(dbPath), "restore-numbers-pending"), []byte("tok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	jumpNumbersAfterRestore(ctx, d.DB, dbPath, now, log)
	jumpNumbersAfterRestore(ctx, d.DB, dbPath, now, log) // file gone: no-op
	if err := os.WriteFile(filepath.Join(filepath.Dir(dbPath), "restore-numbers-pending"), []byte("tok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	jumpNumbersAfterRestore(ctx, d.DB, dbPath, now, log)
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'restore_numbers_jumped'`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("audit rows = %d, %v; want 2 (the first untokened + one for tok, not three)", n, err)
	}
	var floor string
	if err := d.DB.QueryRow(`SELECT value FROM settings WHERE key = 'restore.receipt_floor'`).Scan(&floor); err != nil || floor != "20000" {
		t.Fatalf("receipt floor = %q, %v", floor, err)
	}
}

func TestJumpNumbersAfterRestore_ErrorKeepsMarkerAndDoesNotPanic(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "unitill-pos.db")
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(dbPath), "restore-numbers-pending"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = d.Close() // a closed DB makes the jump fail
	log := &recordingLog{}
	jumpNumbersAfterRestore(context.Background(), d.DB, dbPath, time.Now(), log)
	if len(log.errors) == 0 {
		t.Fatal("expected the failure to be logged")
	}
	if !db.NumberJumpPending(dbPath) {
		t.Fatal("marker must stay so the next boot retries")
	}
}

// The call site follows db.Open in app.Run.
func TestRunCallsTheNumberJumpAfterOpen(t *testing.T) {
	raw, err := os.ReadFile("app.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	i := strings.Index(src, "database, attemptErr = db.Open(cfg.DBPath)")
	k := strings.Index(src, "jumpNumbersAfterRestore(ctx, database.DB, cfg.DBPath")
	if i < 0 || k < i {
		t.Fatal("app.Run must call jumpNumbersAfterRestore after db.Open")
	}
}
