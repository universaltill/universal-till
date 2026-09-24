package testsupport

import (
	"testing"

	"github.com/universaltill/universal-till/internal/db"
)

// TestMigratedDBFile_FullyMigratedAndIsolated pins the two guarantees the
// shared template (ut-docs#2196) must keep: a copy opens through the real
// db.Open with every migration already applied, and each call is its own
// file — a write in one copy never shows in another.
func TestMigratedDBFile_FullyMigratedAndIsolated(t *testing.T) {
	fresh, err := db.Open(t.TempDir() + "/fresh.db")
	if err != nil {
		t.Fatalf("open fresh: %v", err)
	}
	defer fresh.Close()
	var want int
	if err := fresh.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&want); err != nil {
		t.Fatalf("count fresh migrations: %v", err)
	}

	pathA := MigratedDBFile(t, "a.db")
	pathB := MigratedDBFile(t, "a.db")
	if pathA == pathB {
		t.Fatalf("two calls returned the same path %q", pathA)
	}
	a, err := db.Open(pathA)
	if err != nil {
		t.Fatalf("open copy a: %v", err)
	}
	defer a.Close()
	var got int
	if err := a.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&got); err != nil {
		t.Fatalf("count copy migrations: %v", err)
	}
	if got != want || got == 0 {
		t.Fatalf("copy has %d applied migrations, fresh db.Open has %d", got, want)
	}

	if _, err := a.Exec(`CREATE TABLE isolation_probe (x INTEGER)`); err != nil {
		t.Fatalf("write to copy a: %v", err)
	}
	b, err := db.Open(pathB)
	if err != nil {
		t.Fatalf("open copy b: %v", err)
	}
	defer b.Close()
	var n int
	if err := b.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'isolation_probe'`).Scan(&n); err != nil {
		t.Fatalf("probe copy b: %v", err)
	}
	if n != 0 {
		t.Fatal("a table created in copy a is visible in copy b — copies share state")
	}
}
