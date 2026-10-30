package db

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newWatchFixture(t *testing.T) (dbPath, lockPath string) {
	t.Helper()
	dir := t.TempDir()
	dbPath = filepath.Join(dir, "unitill-pos.db")
	lockPath = filepath.Join(dir, dataDirLockName)
	for _, p := range []string{dbPath, lockPath} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return
}

func TestFileWatch_RealFiles(t *testing.T) {
	dbPath, lockPath := newWatchFixture(t)
	w, err := NewFileWatch(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Check(); err != nil {
		t.Fatalf("unchanged: %v", err)
	}

	// Replaced: a different file at the same path.
	other := filepath.Join(filepath.Dir(dbPath), "other")
	if err := os.WriteFile(other, []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(other, lockPath); err != nil {
		t.Fatal(err)
	}
	if err := w.Check(); err == nil || !strings.Contains(err.Error(), lockPath) {
		t.Fatalf("replaced lock file: want error naming %s, got %v", lockPath, err)
	}

	// Removed.
	w2, err := NewFileWatch(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(dbPath); err != nil {
		t.Fatal(err)
	}
	if err := w2.Check(); err == nil || !strings.Contains(err.Error(), dbPath) {
		t.Fatalf("removed db: want error naming %s, got %v", dbPath, err)
	}
}

func TestFileWatch_NewFailsWhenFileMissing(t *testing.T) {
	if _, err := NewFileWatch(filepath.Join(t.TempDir(), "nope.db")); err == nil {
		t.Fatal("want error for missing db")
	}
}

func TestFileWatch_InjectedStat(t *testing.T) {
	dbPath, _ := newWatchFixture(t)
	w, err := NewFileWatch(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	w.stat = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
	if err := w.Check(); err == nil {
		t.Fatal("missing: want error")
	}
	// Same-looking but different identity (the other watched file's info).
	w.stat = func(string) (os.FileInfo, error) { return w.infos[1], nil }
	if err := w.Check(); err == nil {
		t.Fatal("replaced: want error")
	}
}

func TestFileWatch_RunCallsOnLostOnce(t *testing.T) {
	dbPath, _ := newWatchFixture(t)
	w, err := NewFileWatch(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	w.stat = func(string) (os.FileInfo, error) { return nil, errors.New("gone") }
	got := make(chan error, 3)
	done := make(chan struct{})
	go func() {
		w.Run(context.Background(), time.Millisecond, func(e error) { got <- e })
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after loss")
	}
	if len(got) != 1 {
		t.Fatalf("onLost calls = %d, want 1", len(got))
	}
}

func TestFileWatch_RunStopsOnContextDone(t *testing.T) {
	dbPath, _ := newWatchFixture(t)
	w, err := NewFileWatch(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx, time.Millisecond, func(error) { t.Error("onLost called") })
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not exit on ctx.Done")
	}
}

// A transient stat error (not "does not exist") must not stop the till on
// one hiccup, only after fileWatchTransientLimit failed checks in a row.
func TestFileWatch_RunToleratesTransientStatErrors(t *testing.T) {
	dbPath, _ := newWatchFixture(t)
	w, err := NewFileWatch(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	calls := 0
	real := w.stat
	w.stat = func(p string) (os.FileInfo, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		// One failing check (first path) then healthy for a while.
		if calls == 1 {
			return nil, os.ErrPermission
		}
		return real(p)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	w.Run(ctx, time.Millisecond, func(e error) { t.Errorf("onLost after one transient error: %v", e) })

	w.stat = func(string) (os.FileInfo, error) { return nil, os.ErrPermission }
	got := make(chan error, 3)
	w.Run(context.Background(), time.Millisecond, func(e error) { got <- e })
	if len(got) != 1 {
		t.Fatalf("onLost calls = %d, want 1 after repeated transient errors", len(got))
	}
}

func TestFileWatch_CheckNotExistIsNotTransient(t *testing.T) {
	dbPath, _ := newWatchFixture(t)
	w, err := NewFileWatch(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	w.stat = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
	if err := w.Check(); err == nil || errors.Is(err, errStatFailed) {
		t.Fatalf("not-exist: want a non-transient error, got %v", err)
	}
	w.stat = func(string) (os.FileInfo, error) { return nil, os.ErrPermission }
	if err := w.Check(); !errors.Is(err, errStatFailed) {
		t.Fatalf("permission: want errStatFailed, got %v", err)
	}
}
