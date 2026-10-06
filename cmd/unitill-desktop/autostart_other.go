//go:build desktop && !linux && !windows

package main

// reconcileAutostart is a no-op on macOS until ut-docs#609 wires its
// LaunchAgent; Linux and Windows (ut-docs#610) have real ones.
func reconcileAutostart(enabled bool) error {
	return nil
}
