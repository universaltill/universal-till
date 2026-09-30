//go:build ios || android

package procrestart

import "errors"

// supported: an app embedding the till as a gomobile library can't re-exec
// (iOS forbids exec; on Android it would replace the whole app process), so
// only the in-process restarter the mobile package registers makes
// Supported() true there (ut-docs#3220).
const supported = false

// ErrUnsupported is what reexec answers on iOS/Android. Restart uses the
// registered restarter there instead; without one Supported() is false and
// callers never reach this.
var ErrUnsupported = errors.New("in-place process restart isn't available inside a mobile app; close and reopen Universal Till")

func reexec(_ string) error {
	return ErrUnsupported
}
