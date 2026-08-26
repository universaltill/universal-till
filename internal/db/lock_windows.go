//go:build windows

package db

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// errLockHeld is the sentinel tryLockExclusive returns when the lock is
// already held elsewhere, so lock.go's AcquireDataDirLock can translate it
// into the platform-independent ErrDataDirLocked.
var errLockHeld = errors.New("lock held by another process")

// tryLockExclusive takes a non-blocking exclusive lock on f's file handle
// via LockFileEx. Released automatically when f is closed or the process
// exits — no separate unlock call needed.
func tryLockExclusive(f *os.File) error {
	ol := new(windows.Overlapped)
	err := windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, ol,
	)
	if err == nil {
		return nil
	}
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return errLockHeld
	}
	return err
}
