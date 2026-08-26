//go:build linux || darwin

package db

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// errLockHeld is the sentinel tryLockExclusive returns when the lock is
// already held elsewhere, so lock.go's AcquireDataDirLock can translate it
// into the platform-independent ErrDataDirLocked.
var errLockHeld = errors.New("lock held by another process")

// tryLockExclusive takes a non-blocking exclusive advisory lock on f's file
// descriptor via flock(2). Released automatically when f is closed or the
// process exits/is killed — no separate unlock call needed.
func tryLockExclusive(f *os.File) error {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err == nil {
		return nil
	}
	if errors.Is(err, unix.EWOULDBLOCK) {
		return errLockHeld
	}
	return err
}
