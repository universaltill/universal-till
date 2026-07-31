package data

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestSettingsRepo_SetAndGet(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT, updated_at TEXT)`); err != nil {
		t.Fatalf("create settings: %v", err)
	}

	repo := NewSettingsRepo(db)
	ctx := context.Background()

	// Missing key returns ok=false
	if val, ok, err := repo.Get(ctx, "missing"); err != nil || ok || val != "" {
		t.Fatalf("expected missing key, got val=%q ok=%v err=%v", val, ok, err)
	}

	if err := repo.Set(ctx, "site.name", "Unitill"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	val, ok, err := repo.Get(ctx, "site.name")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok || val != "Unitill" {
		t.Fatalf("unexpected get result val=%q ok=%v", val, ok)
	}

	// Ensure updated_at is written
	var updatedAt string
	if err := db.QueryRow(`SELECT updated_at FROM settings WHERE key='site.name'`).Scan(&updatedAt); err != nil {
		t.Fatalf("scan updated_at: %v", err)
	}
	if updatedAt == "" {
		t.Fatal("expected updated_at to be set")
	}
}

func newSettingsTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT, updated_at TEXT)`); err != nil {
		t.Fatalf("create settings: %v", err)
	}
	return db
}

func TestSettingsRepo_SetMany(t *testing.T) {
	db := newSettingsTestDB(t)
	repo := NewSettingsRepo(db)
	ctx := context.Background()

	if err := repo.Set(ctx, "a.existing", "old"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	if err := repo.SetMany(ctx, map[string]string{
		"a.existing": "new",
		"b.fresh":    "1",
		"c.fresh":    "2",
	}); err != nil {
		t.Fatalf("SetMany: %v", err)
	}

	for key, want := range map[string]string{"a.existing": "new", "b.fresh": "1", "c.fresh": "2"} {
		val, ok, err := repo.Get(ctx, key)
		if err != nil || !ok || val != want {
			t.Fatalf("Get(%s) = %q ok=%v err=%v, want %q", key, val, ok, err, want)
		}
	}

	// Upsert, not duplicate: a.existing must still be a single row.
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM settings WHERE key='a.existing'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("a.existing has %d rows, want 1", n)
	}
}

// ut-docs#12: SetMany must be all-or-nothing. A trigger aborts the write of
// one key that sorts mid-batch; keys sorted before it must be rolled back,
// not left behind as a partial write.
func TestSettingsRepo_SetManyAtomic(t *testing.T) {
	db := newSettingsTestDB(t)
	if _, err := db.Exec(`
CREATE TRIGGER boom BEFORE INSERT ON settings
WHEN NEW.key = 'm.boom'
BEGIN SELECT RAISE(ABORT, 'injected failure'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	repo := NewSettingsRepo(db)
	ctx := context.Background()

	err := repo.SetMany(ctx, map[string]string{
		"a.first": "1", // sorts before m.boom — written, then must roll back
		"m.boom":  "x",
		"z.last":  "3",
	})
	if err == nil {
		t.Fatal("SetMany with an aborting trigger must return an error")
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM settings`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("settings has %d rows after failed SetMany, want 0 (partial write not rolled back)", n)
	}
}
