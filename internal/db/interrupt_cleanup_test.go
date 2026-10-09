package db

import (
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ut-docs#4000: removeAllEventually must outlast a writer that keeps
// creating files in the directory for a while — the shape of the late
// SQLite connection that database/sql's background opener can still be
// opening (and so creating -wal/-shm) after DB.Close returns. A one-shot
// os.RemoveAll fails here with "directory not empty", exactly as
// t.TempDir's cleanup did on main.
func TestRemoveAllEventuallyOutlastsALateWriter(t *testing.T) {
	dir, err := os.MkdirTemp("", "remove-eventually-")
	if err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	var (
		stop    atomic.Bool
		wg      sync.WaitGroup
		written atomic.Int64
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; !stop.Load(); i++ {
			// Errors are expected once the directory is gone: WriteFile
			// never recreates a parent, so the writer cannot resurrect it.
			if os.WriteFile(filepath.Join(dir, "f"+strconv.Itoa(i)), []byte("x"), 0o600) == nil {
				written.Add(1)
			}
		}
	}()
	defer func() {
		stop.Store(true)
		wg.Wait()
	}()
	// Let the writer get going, then stop it shortly after removal starts:
	// it is a bounded burst, like a pool's at most MaxOpenConns late opens.
	for written.Load() < 50 {
		time.Sleep(time.Millisecond)
	}
	go func() {
		time.Sleep(300 * time.Millisecond)
		stop.Store(true)
	}()

	if err := removeAllEventually(dir, 10*time.Second); err != nil {
		t.Fatalf("removeAllEventually: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("directory still exists after removeAllEventually (stat err %v)", err)
	}
}

func TestRemoveAllEventuallyGivesUpAfterItsDeadline(t *testing.T) {
	// A NUL byte makes every RemoveAll attempt fail (EINVAL), so the helper
	// must retry until its deadline and then return that error — never spin
	// forever, never swallow it.
	const timeout = 200 * time.Millisecond
	start := time.Now()
	err := removeAllEventually("never\x00removable", timeout)
	took := time.Since(start)
	if err == nil {
		t.Fatal("removeAllEventually returned nil for a path RemoveAll always refuses")
	}
	if took < timeout {
		t.Fatalf("gave up after %v, before its %v deadline", took, timeout)
	}
	if took > timeout+2*time.Second {
		t.Fatalf("took %v with a %v deadline", took, timeout)
	}
}
