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
