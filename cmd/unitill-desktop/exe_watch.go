package main

import (
	"context"
	"fmt"
	"os"
	"time"
)

// watchOwnBinary is the pure half of the shell's self-re-exec on Linux
// (ut-docs#2991, AC1): an apt/.deb upgrade replaces
// /opt/unitill/bin/unitill-desktop on disk while the old shell keeps
// running from the now-deleted inode (/proc/<pid>/exe -> "... (deleted)"),
// old WebKit/GTK libraries and all, until its web process eventually dies
// on screen. internal/selfupdate never swaps this binary (only unitill-pos
// + web/, and the web UI already reloads itself after that restart), so
// the package manager is the only thing that changes it underneath us.
//
// It records path's FileInfo once, then re-stats on every tick and calls
// onChanged exactly once — then returns nil — when path names a DIFFERENT
// file than at start (another inode, or same inode with a new size/mtime)
// AND that new file looked identical on two consecutive checks: dpkg writes
// a .dpkg-new and renames it over, but a half-written in-place copy must
// not be exec'd. A stat error (the path briefly missing mid-dpkg) is "no
// change yet" and restarts the debounce; it never fires. Returns nil on ctx
// cancel, or an error only when the initial stat fails (no baseline).
//
// ticks and stat are injected so exe_watch_test.go drives it
// deterministically; checked (nil in production) is a test hook called
// after the baseline and after every completed check.
func watchOwnBinary(ctx context.Context, path string, ticks <-chan time.Time, stat func(string) (os.FileInfo, error), onChanged, checked func()) error {
	done := func() {
		if checked != nil {
			checked()
		}
	}
	initial, err := stat(path)
	if err != nil {
		return fmt.Errorf("stat own binary %q: %w", path, err)
	}
	done()
	var candidate os.FileInfo // first sighting of a changed file, awaiting confirmation
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticks:
		}
		cur, err := stat(path)
		switch {
		case err != nil:
			candidate = nil
		case !fileChanged(initial, cur):
			candidate = nil
		case candidate != nil && !fileChanged(candidate, cur):
			onChanged()
			done()
			return nil
		default:
			candidate = cur
		}
		done()
	}
}

// fileChanged reports whether b is not the same on-disk file as a, or is
// the same inode with different contents as far as size/mtime can tell.
func fileChanged(a, b os.FileInfo) bool {
	return !os.SameFile(a, b) || a.Size() != b.Size() || !a.ModTime().Equal(b.ModTime())
}
