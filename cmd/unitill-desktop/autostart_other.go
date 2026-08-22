//go:build desktop && !linux

package main

// reconcileAutostart is a no-op on non-Linux desktop shells (ut-docs#611
// covers Linux only) — macOS (LaunchAgent) and Windows (Run key/Startup
// shortcut) autostart wiring is #609/#610's own scope.
func reconcileAutostart(enabled bool) error {
	return nil
}
