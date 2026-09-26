//go:build windows

package selfupdate

// Windows is filtered out by Supported() (it updates via the installer), so
// this never runs; it exists only so the package compiles on Windows.
func reexec(_ string) error {
	return ErrUnsupported
}

// signalSelf is never reached on Windows (see reexec above).
func signalSelf() error {
	return ErrUnsupported
}
