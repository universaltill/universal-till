package main

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// watchOwnBinary (ut-docs#2991, AC1) is untagged like shell_poll.go so plain
// `go test ./...` exercises it. The tests use real files in t.TempDir() —
// os.SameFile only works on FileInfo values os.Stat itself returned — and
// drive the checks through an unbuffered tick channel so every assertion
// sees a deterministic number of completed checks.

type exeWatchHarness struct {
	t       *testing.T
	path    string
	ticks   chan time.Time
	fired   chan struct{}
	done    chan error
	cancel  context.CancelFunc
	checked chan struct{} // one value per completed check (initial baseline included)
	mu      sync.Mutex
	statErr error // when non-nil, stat returns this instead of os.Stat
}

func newExeWatchHarness(t *testing.T) *exeWatchHarness {
	t.Helper()
	dir := t.TempDir()
	h := &exeWatchHarness{
		t:     t,
		path:  filepath.Join(dir, "unitill-desktop"),
		ticks: make(chan time.Time),
		fired: make(chan struct{}, 4),
		done:  make(chan error, 1),
	}
	writeFile(t, h.path, "v1 binary")
	return h
}

func (h *exeWatchHarness) stat(p string) (os.FileInfo, error) {
	h.mu.Lock()
	err := h.statErr
	h.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return os.Stat(p)
}

func (h *exeWatchHarness) setStatErr(err error) {
	h.mu.Lock()
	h.statErr = err
	h.mu.Unlock()
}

func (h *exeWatchHarness) start() {
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	h.t.Cleanup(cancel)
	h.checked = make(chan struct{}, 1)
	go func() {
		h.done <- watchOwnBinary(ctx, h.path, h.ticks, h.stat, func() { h.fired <- struct{}{} }, func() { h.checked <- struct{}{} })
	}()
	select {
	case <-h.checked:
	case err := <-h.done:
		h.t.Fatalf("watchOwnBinary returned before its first check: %v", err)
	case <-time.After(5 * time.Second):
		h.t.Fatal("watchOwnBinary never recorded its initial fingerprint")
	}
}

// tick delivers one check and waits until the watcher has finished it, so
// every assertion after it sees a deterministic number of completed checks.
// A watcher that already returned (it fired) is not an error here.
func (h *exeWatchHarness) tick() {
	h.t.Helper()
	select {
	case h.ticks <- time.Now():
	case err := <-h.done:
		h.done <- err
		return
	case <-time.After(5 * time.Second):
		h.t.Fatal("watcher did not accept a tick")
	}
	select {
	case <-h.checked:
	case <-time.After(5 * time.Second):
		h.t.Fatal("watcher did not finish a check")
	}
}

func (h *exeWatchHarness) firedCount() int {
	return len(h.fired)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

// replaceFile mimics dpkg: write a new inode beside the target and rename it
// over, so the running binary's path now names a different file.
func replaceFile(t *testing.T, path, content string) {
	t.Helper()
	tmp := path + ".dpkg-new"
	writeFile(t, tmp, content)
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

func TestWatchOwnBinary_UnchangedNeverFires(t *testing.T) {
	h := newExeWatchHarness(t)
	h.start()
	for range 5 {
		h.tick()
	}
	if n := h.firedCount(); n != 0 {
		t.Fatalf("fired %d times for an unchanged binary", n)
	}
}

func TestWatchOwnBinary_ReplacedAndStableFiresOnce(t *testing.T) {
	h := newExeWatchHarness(t)
	h.start()
	replaceFile(t, h.path, "v2 binary, longer")
	h.tick() // first sighting: candidate only
	if n := h.firedCount(); n != 0 {
		t.Fatalf("fired on the first sighting of the new file (%d), want debounce", n)
	}
	h.tick() // second, identical sighting: fire
	select {
	case <-h.fired:
	case <-time.After(5 * time.Second):
		t.Fatal("did not fire for a replaced, stable binary")
	}
	select {
	case err := <-h.done:
		if err != nil {
			t.Fatalf("watchOwnBinary() = %v, want nil after firing", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("watcher kept running after firing — must fire at most once")
	}
}

func TestWatchOwnBinary_InPlaceRewriteFires(t *testing.T) {
	h := newExeWatchHarness(t)
	h.start()
	writeFile(t, h.path, "rewritten in place, different size")
	h.tick()
	h.tick()
	select {
	case <-h.fired:
	case <-time.After(5 * time.Second):
		t.Fatal("did not fire for a binary rewritten in place")
	}
}

func TestWatchOwnBinary_StillChangingWaits(t *testing.T) {
	h := newExeWatchHarness(t)
	h.start()
	replaceFile(t, h.path, "v2 partial")
	h.tick()
	replaceFile(t, h.path, "v2 partial, more bytes")
	h.tick()
	replaceFile(t, h.path, "v2 partial, even more bytes now")
	h.tick()
	if n := h.firedCount(); n != 0 {
		t.Fatalf("fired %d times while the file was still changing", n)
	}
	h.tick() // this 4th check sees the same file as the 3rd → fires
	select {
	case <-h.fired:
	case <-time.After(5 * time.Second):
		t.Fatal("did not fire once the file settled")
	}
	if n := h.firedCount(); n != 0 {
		t.Fatalf("fired %d extra times", n)
	}
}

func TestWatchOwnBinary_StillChangingDoesNotFireEarly(t *testing.T) {
	h := newExeWatchHarness(t)
	h.start()
	for i := range 4 {
		replaceFile(t, h.path, "v2 partial"+string(rune('a'+i)))
		h.tick()
	}
	replaceFile(t, h.path, "v2 partial final and longer")
	h.tick()
	if n := h.firedCount(); n != 0 {
		t.Fatalf("fired %d times while the file kept changing", n)
	}
}

func TestWatchOwnBinary_MissingFileDoesNotFire(t *testing.T) {
	h := newExeWatchHarness(t)
	h.start()
	if err := os.Remove(h.path); err != nil {
		t.Fatal(err)
	}
	for range 4 {
		h.tick()
	}
	h.setStatErr(errors.New("transient I/O error"))
	for range 3 {
		h.tick()
	}
	if n := h.firedCount(); n != 0 {
		t.Fatalf("fired %d times for a missing/unreadable file", n)
	}
}

func TestWatchOwnBinary_MissingBetweenSightingsResetsDebounce(t *testing.T) {
	h := newExeWatchHarness(t)
	h.start()
	replaceFile(t, h.path, "v2")
	h.tick() // candidate
	h.setStatErr(fs.ErrNotExist)
	h.tick() // gone again: not stable
	h.setStatErr(nil)
	h.tick() // candidate again (same file as before, but debounce restarted)
	if n := h.firedCount(); n != 0 {
		t.Fatalf("fired %d times right after the file reappeared, want debounce restart", n)
	}
	h.tick() // 2nd consecutive identical sighting → fires now
	select {
	case <-h.fired:
	case <-time.After(5 * time.Second):
		t.Fatal("did not fire after the file came back and stayed")
	}
}

func TestWatchOwnBinary_ContextCancelStops(t *testing.T) {
	h := newExeWatchHarness(t)
	h.start()
	h.tick()
	h.cancel()
	select {
	case err := <-h.done:
		if err != nil {
			t.Fatalf("watchOwnBinary() = %v on cancel, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not stop on context cancel")
	}
	if n := h.firedCount(); n != 0 {
		t.Fatalf("fired %d times", n)
	}
}

func TestWatchOwnBinary_InitialStatFailureReturnsError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope")
	err := watchOwnBinary(context.Background(), missing, make(chan time.Time), os.Stat, func() {
		t.Fatal("fired without a baseline")
	}, nil)
	if err == nil {
		t.Fatal("watchOwnBinary() = nil for an unstat-able path, want an error")
	}
}
