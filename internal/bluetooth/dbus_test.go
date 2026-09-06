package bluetooth

import (
	"errors"
	"testing"
)

// TestNewDBusClientFor_AndroidIsUnsupportedPlatform (ut-docs#1643): Android
// has no D-Bus system bus and no bluetoothd, so this must fail closed with
// ErrUnsupportedPlatform *before* ever calling dbus.ConnectSystemBus() — not
// ErrUnavailable, which the page layer renders as "no adapter found, or the
// service is not running." That message is actively wrong on Android, where
// the radio is present and healthy (confirmed via dumpsys on the reporting
// device); the feature just isn't implemented for this platform yet.
func TestNewDBusClientFor_AndroidIsUnsupportedPlatform(t *testing.T) {
	_, err := newDBusClientFor("android")
	if !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("newDBusClientFor(\"android\") = %v, want ErrUnsupportedPlatform", err)
	}
}

// TestNewDBusClientFor_LinuxAttemptsRealConnection guards the other side of
// the gate: a non-Android GOOS must still take the real D-Bus path (and so
// fail with something other than ErrUnsupportedPlatform in this CI sandbox,
// which has no system bus) — the platform gate must not over-match and
// swallow every OS.
func TestNewDBusClientFor_LinuxAttemptsRealConnection(t *testing.T) {
	_, err := newDBusClientFor("linux")
	if err == nil {
		return // an unlikely CI sandbox with a real system bus — fine either way
	}
	if errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("linux must not be treated as an unsupported platform, got %v", err)
	}
}
