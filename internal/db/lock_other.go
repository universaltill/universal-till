//go:build !linux && !darwin && !windows

package db

import (
	"errors"
	"os"
)

// errLockHeld exists on this platform build only so lock.go compiles
// identically everywhere; it is never actually returned here (see below).
var errLockHeld = errors.New("lock held by another process")

// tryLockExclusive is a no-op on every remaining GOOS this module targets —
// concretely, android and ios (mobile/mobile.go, ADR-0023's gomobile-bind
// shell). The multi-process race ut-docs#1097 fixes cannot happen there: a
// mobile build runs the whole boot sequence IN-PROCESS inside the single
// host app (that's the entire point of internal/app.Run being factored out
// for mobile.go to call directly), so there is no second OS process that
// could ever race to open the same data directory the way a desktop shell
// spawning a sibling unitill-pos binary can. A real lock here would add
// nothing but a false sense that this file is where the guarantee comes
// from — it isn't, the single-process architecture already is.
func tryLockExclusive(f *os.File) error {
	return nil
}
