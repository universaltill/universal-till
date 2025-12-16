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
