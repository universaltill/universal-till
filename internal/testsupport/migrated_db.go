package testsupport

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
)

// Replaying every migration through db.Open costs ~0.18s per call, ~5s
// under -race; internal/data's tests did it ~100 times per run
// (ut-docs#2196). The schema is identical every time, so it is built once
// per test binary and kept in memory; each caller gets its own file copy.
var (
	migratedTemplateOnce sync.Once
	migratedTemplate     []byte
	migratedTemplateErr  error
)

func buildMigratedTemplate() ([]byte, error) {
	dir, err := os.MkdirTemp("", "unitill-migrated-template-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "template.db")
	d, err := db.Open(path)
	if err != nil {
		return nil, fmt.Errorf("migrate template: %w", err)
	}
	// Close checkpoints the WAL back into the main file, so the one file
	// read below is the whole database.
	if err := d.Close(); err != nil {
		return nil, fmt.Errorf("close template: %w", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read template: %w", err)
	}
	return raw, nil
}

// MigratedDBFile writes a fresh copy of a fully migrated database to
// t.TempDir()/name and returns its path, for the caller to pass to
// db.Open — which then finds every migration applied and only verifies
// the ledger. Each call is a separate file, so tests stay as isolated as
// with a per-test db.Open on an empty path.
func MigratedDBFile(t testing.TB, name string) string {
	t.Helper()
	// A failed build is sticky: every later caller fails with the same
	// error. It can only fail if the migrations themselves are broken,
	// which would fail every such test anyway.
	migratedTemplateOnce.Do(func() {
		migratedTemplate, migratedTemplateErr = buildMigratedTemplate()
	})
	if migratedTemplateErr != nil {
		t.Fatalf("build migrated template db: %v", migratedTemplateErr)
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create migrated db dir: %v", err)
	}
	if err := os.WriteFile(path, migratedTemplate, 0o600); err != nil {
		t.Fatalf("write migrated db copy: %v", err)
	}
	return path
}
