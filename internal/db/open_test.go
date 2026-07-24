package db

import (
	"os"
	"path/filepath"
	"testing"
)

// A fresh install extracts to a folder with no data/ directory. Opening the
// default ./data/unitill-pos.db path must create that directory rather than
// fail with SQLite CANTOPEN ("out of memory (14)") and crash on first run.
func TestOpenCreatesMissingDataDir(t *testing.T) {
	base := filepath.Join(t.TempDir(), "extracted")
	// Neither base/ nor base/data/ exists yet — mirrors a fresh download.
	path := filepath.Join(base, "data", "unitill-pos.db")

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open on a fresh install must succeed, got: %v", err)
	}
	defer d.Close()

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database file was not created: %v", err)
	}
}

// ADR-0020: self-order kiosk sales attribute to a fixed, well-known "kiosk"
// user (not a session — the kiosk route is auth-exempt). sales.cashier_id
// has a real FK to users(id), so this row must exist on every till,
// low-privilege (role=cashier, never manager) so it can never pass an
// isManagerOrAuthOff check if ever probed.
func TestKioskUserSeeded(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "kiosk.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	var role string
	var active int
	if err := d.DB.QueryRow(`SELECT role, is_active FROM users WHERE id = 'kiosk'`).Scan(&role, &active); err != nil {
		t.Fatalf("kiosk user not seeded: %v", err)
	}
	if role != "cashier" || active != 1 {
		t.Fatalf("unexpected kiosk user: role=%q active=%d, want role=cashier active=1", role, active)
	}
}
