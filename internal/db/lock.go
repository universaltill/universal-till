package db

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrDataDirLocked is returned by AcquireDataDirLock when another process
// already holds the exclusive lock on this data directory.
var ErrDataDirLocked = errors.New("data directory is already locked by another running instance")

// dataDirLockName is a dotfile sibling to the DB file, not inside it: the DB
// itself is a SQLite file with its own separate locking domain
// (-wal/-shm, SQLite's own advisory locks between connections in the SAME
// process); this lock is a coarser, earlier gate — which unitill-pos
// PROCESS may open this data directory at all — checked before db.Open ever
// touches the .db file.
//
// Scoped to filepath.Dir(dbPath), the same way every other per-till artefact
// in this package is keyed (backup.go's backups/ directory, replica.go's
// replica-identity and pending-restore markers). With the default config
// that IS paths.DataDir(); with UT_DB_PATH pointing the database somewhere
// else, the lock follows the DATABASE — the resource whose concurrent
// writers caused ut-docs#1097 — and so does every one of those siblings,
// which is exactly the consistency wanted here.
const dataDirLockName = ".unitill.lock"

// DataDirLock is an exclusive, advisory, whole-process lock on a till's data
// directory, held for the life of the process that acquired it. Backed by
// the OS's own advisory file lock (flock/LockFileEx, per platform — see
// lock_unix.go/lock_windows.go/lock_other.go), not a PID-in-a-file
// convention: a PID file needs its own staleness/liveness check (is that
// PID still alive? on THIS machine? not reused by an unrelated process?) and
// gets that wrong in exactly the crash-without-cleanup case that matters
// most. An OS-level lock has none of that — it is released automatically
// the instant the holding process exits, cleanly or via SIGKILL, no
// second-guessing required.
type DataDirLock struct {
	f *os.File
}

// AcquireDataDirLock takes the exclusive lock for the directory containing
// dbPath (ut-docs#1097). The bug this closes: the desktop shell used to
// decide "is a server already running against this data" with a single,
// timeout-bounded /healthz probe — too weak a signal, because a restart
// window, a slow boot, or a shell launched a beat too early all read as
// "nothing is listening." That let the shell spawn a second unitill-pos
// against the SAME SQLite database (real incident: two live servers for
// ~19 minutes, the second one grabbing :8080 first and pushing the real
// systemd-managed service to :8081 via listenWithFallback, silently — see
// the issue for the field log). A port probe answers "is something
// listening on this port"; it can never answer "does something already own
// this DATA," which is the question that actually matters.
//
// This lock makes the failure mode structurally impossible instead of
// merely unlikely: whichever process gets here first owns the data
// directory for as long as it runs, and every other unitill-pos that tries
// to open the SAME data directory gets ErrDataDirLocked back immediately
// and must refuse to start — not silently pick a different port and run
// anyway. Call sites are expected to treat ErrDataDirLocked as fatal and
// say so plainly (see internal/app.Run), never to retry a different address.
func AcquireDataDirLock(dbPath string) (*DataDirLock, error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create data directory %s: %w", dir, err)
	}
	path := filepath.Join(dir, dataDirLockName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file %s: %w", path, err)
	}
	if err := tryLockExclusive(f); err != nil {
		_ = f.Close()
		if errors.Is(err, errLockHeld) {
			return nil, ErrDataDirLocked
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	// Best-effort breadcrumb for a human reading the file mid-incident —
	// never read back programmatically. The OS-level lock, not this content,
	// is the actual source of truth for who owns the directory.
	_ = f.Truncate(0)
	_, _ = f.Seek(0, 0)
	_, _ = fmt.Fprintf(f, "pid=%d\n", os.Getpid())
	return &DataDirLock{f: f}, nil
}

// Release drops the lock and closes the underlying file handle, which is
// what actually releases the OS-level lock. Safe to call on a nil receiver
// (mirrors how callers already `defer lock.Release()` unconditionally after
// a successful Acquire); calling it twice on the same lock is not
// supported, same as the *os.File.Close it wraps.
//
// Deliberately does NOT delete the lock file. Unlinking it would open the
// one race an OS-level lock otherwise doesn't have: a second process that
// already opened the same path would end up holding a lock on the now-
// unlinked inode while a third opens a freshly created file — and both
// would succeed. A leftover zero-cost dotfile is the correct trade; a stale
// file conveys no ownership on its own (the OS lock does), which is why a
// SIGKILLed till's leftover .unitill.lock does not block the next start.
func (l *DataDirLock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	return l.f.Close()
}
