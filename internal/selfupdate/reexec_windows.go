//go:build windows

package selfupdate

// Windows never re-execs: Apply hands over to the installer helper instead
// (wininstaller.go), so this never runs; it exists only so the package
// compiles on Windows.
func reexec(_ string) error {
	return ErrUnsupported
}

// signalSelf is never reached on Windows (see reexec above).
func signalSelf() error {
	return ErrUnsupported
}
