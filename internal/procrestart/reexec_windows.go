//go:build windows

package procrestart

import "errors"

// supported: Windows has no in-place exec, so Supported() is false and the
// UI shows the "close and reopen" instruction instead (ut-docs#1614 tracks
// a native Windows restart).
const supported = false

// ErrUnsupported is what reexec answers on Windows. Supported() is false
// there, so callers never actually reach it; it exists only so the package
// compiles on Windows.
var ErrUnsupported = errors.New("in-place process restart isn't available on Windows; close and reopen Universal Till")

func reexec(_ string) error {
	return ErrUnsupported
}
