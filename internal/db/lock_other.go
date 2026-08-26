//go:build !linux && !darwin && !windows

package db

import (
	"errors"
	"os"
)

// errLockHeld exists on this platform build only so lock.go compiles
// identically everywhere; it is never actually returned here (see below).
var errLockHeld = errors.New("lock held by another process")

// tryLockExclusive is a no-op fallback for GOOS values this module does not
// ship to — freebsd, plan9, js/wasm and friends. It is a compile-anywhere
// stub, NOT a reasoned exemption for any platform we actually ship.
//
// In particular it is NOT the mobile path, despite what the constraint
// might look like at a glance: Go treats GOOS=android as also satisfying
// the `linux` build tag and GOOS=ios as also satisfying `darwin`, so the
// Android/iOS shells (mobile/mobile.go, ADR-0023's gomobile-bind build)
// compile lock_unix.go and get the real flock, same as desktop Linux/macOS.
// That is the right outcome and costs them nothing: mobile.Stop blocks
// until app.Run has fully returned (its deferred Release included) before
// a subsequent Start can run, and mobile.Start is idempotent while a server
// is genuinely up — so the real lock never spuriously refuses a
// background/foreground Stop-then-Start cycle, and does still catch a host
// shell that manages to drive two overlapping Runs at the same data
// directory.
//
// Verify with: GOOS=android go list -f '{{.GoFiles}}' ./internal/db
func tryLockExclusive(f *os.File) error {
	return nil
}
