package app

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
)

// Migrating a brand-new database is the dominant cost of every Run() boot
// test: ~12s of a ~13s boot under -race (ut-docs#7275 profile — every ALTER
// TABLE makes SQLite re-parse the whole schema, and 132/134 add 20 columns:
// 2.5s of the slowdown that #2765 introduced). The migrations are shipped
// and append-only (ADR-0100), so they cannot be reshaped; instead the tests
// that are about something other than migrating start from a database that
// is already at the latest version. Run still goes through db.Open on it —
// ledger verification, lineage check and the "nothing to apply" path are all
// exercised — only the repeated DDL is paid once per test binary.
var (
	migratedTemplateOnce sync.Once
	migratedTemplate     []byte
	migratedTemplateErr  error
)

func buildMigratedTemplate() ([]byte, error) {
	dir, err := os.MkdirTemp("", "ut-app-template-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "unitill-pos.db")
	d, err := db.Open(path)
	if err != nil {
		return nil, err
	}
	if err := d.Close(); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

// seedMigratedDB drops an already-migrated unitill-pos.db into dataDir (the
// default UT_DB_PATH location) so Run's db.Open has no migrations to apply.
func seedMigratedDB(t *testing.T, dataDir string) {
	t.Helper()
	migratedTemplateOnce.Do(func() { migratedTemplate, migratedTemplateErr = buildMigratedTemplate() })
	if migratedTemplateErr != nil {
		t.Fatalf("build migrated template database: %v", migratedTemplateErr)
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "unitill-pos.db"), migratedTemplate, 0o644); err != nil {
		t.Fatal(err)
	}
}

// The seeded file must be what Open treats as fully migrated: reopening it
// applies nothing and takes a fraction of a fresh migration.
func TestSeedMigratedDB_OpensWithNothingToApply(t *testing.T) {
	dir := t.TempDir()
	seedMigratedDB(t, dir)
	d, err := db.Open(filepath.Join(dir, "unitill-pos.db"))
	if err != nil {
		t.Fatalf("open seeded db: %v", err)
	}
	defer d.Close()
	var top int
	if err := d.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&top); err != nil || top < 134 {
		t.Fatalf("ledger top version = %d, err = %v; want the latest shipped migration (>= 134)", top, err)
	}
}
