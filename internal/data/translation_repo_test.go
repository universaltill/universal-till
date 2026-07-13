package data

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func newTranslationTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE translation_overrides (
		locale TEXT NOT NULL, key TEXT NOT NULL, value TEXT NOT NULL,
		updated_at TEXT NOT NULL, updated_by TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (locale, key))`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	return db
}

func TestTranslationRepoRoundTrip(t *testing.T) {
	repo := NewTranslationRepo(newTranslationTestDB(t))
	ctx := context.Background()

	if err := repo.SetOverride(ctx, "fa", "basket.total", "جمع کل", "admin", "2026-07-14T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	// upsert replaces
	if err := repo.SetOverride(ctx, "fa", "basket.total", "جمع نهایی", "admin", "2026-07-14T00:01:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetOverride(ctx, "en", "basket.total", "Grand total", "admin", "2026-07-14T00:02:00Z"); err != nil {
		t.Fatal(err)
	}

	overrides, err := repo.ListOverrides(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if overrides["fa"]["basket.total"] != "جمع نهایی" {
		t.Fatalf("fa override = %q", overrides["fa"]["basket.total"])
	}
	if overrides["en"]["basket.total"] != "Grand total" {
		t.Fatalf("en override = %q", overrides["en"]["basket.total"])
	}

	existed, err := repo.ClearOverride(ctx, "fa", "basket.total")
	if err != nil || !existed {
		t.Fatalf("clear: existed=%v err=%v", existed, err)
	}
	existed, err = repo.ClearOverride(ctx, "fa", "basket.total")
	if err != nil || existed {
		t.Fatalf("second clear must report not-found, existed=%v err=%v", existed, err)
	}
}
