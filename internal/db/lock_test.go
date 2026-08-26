package db

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// The core guarantee ut-docs#1097 exists for: two attempts to acquire the
// SAME data directory's lock must never both succeed, no matter how they
// race — a second unitill-pos process against the same data directory must
// refuse to start, not silently proceed. Uses a real dbPath under a real
// temp dir (mirrors testDBPath's shape from backup_test.go) so this
// exercises the actual platform lock implementation (lock_unix.go's flock
// on the CI runner), not a fake.
func TestAcquireDataDirLock_SecondAcquireFails(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "unitill-pos.db")

	first, err := AcquireDataDirLock(dbPath)
	if err != nil {
		t.Fatalf("first AcquireDataDirLock: %v", err)
	}
	t.Cleanup(func() { _ = first.Release() })

	if _, err := AcquireDataDirLock(dbPath); !errors.Is(err, ErrDataDirLocked) {
		t.Fatalf("second AcquireDataDirLock = %v, want ErrDataDirLocked", err)
	}
}

// Releasing the first holder must free the lock for the next acquirer —
// otherwise a clean shutdown-then-restart (the overwhelmingly common case,
// not the crash this lock mainly guards against) would wrongly refuse to
// start.
func TestAcquireDataDirLock_ReleaseAllowsReacquire(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "unitill-pos.db")

	first, err := AcquireDataDirLock(dbPath)
	if err != nil {
		t.Fatalf("first AcquireDataDirLock: %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}

	second, err := AcquireDataDirLock(dbPath)
	if err != nil {
		t.Fatalf("AcquireDataDirLock after Release: %v", err)
	}
	_ = second.Release()
}

// Two DIFFERENT data directories must never contend with each other — a
// lock scoped too broadly (e.g. a single well-known path instead of one
// per data directory) would wrongly block two genuinely independent tills
// (or, in CI, two genuinely independent tests) from ever running at once.
func TestAcquireDataDirLock_DifferentDirectoriesDoNotContend(t *testing.T) {
	pathA := filepath.Join(t.TempDir(), "unitill-pos.db")
	pathB := filepath.Join(t.TempDir(), "unitill-pos.db")

	a, err := AcquireDataDirLock(pathA)
	if err != nil {
		t.Fatalf("AcquireDataDirLock(A): %v", err)
	}
	t.Cleanup(func() { _ = a.Release() })

	b, err := AcquireDataDirLock(pathB)
	if err != nil {
		t.Fatalf("AcquireDataDirLock(B) was blocked by an unrelated directory's lock: %v", err)
	}
	_ = b.Release()
}

// The data directory may not exist yet on a fresh install (nothing has
// written a DB file there at all) — AcquireDataDirLock must create it
// rather than failing, since it runs before db.Open today.
func TestAcquireDataDirLock_CreatesMissingDataDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "not-yet-created")
	dbPath := filepath.Join(dir, "unitill-pos.db")

	lock, err := AcquireDataDirLock(dbPath)
	if err != nil {
		t.Fatalf("AcquireDataDirLock on a missing directory: %v", err)
	}
	defer lock.Release()

	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("expected %s to exist as a directory, stat: %v", dir, err)
	}
}
