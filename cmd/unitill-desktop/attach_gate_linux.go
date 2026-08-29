//go:build desktop && linux

package main

import "time"

// attachDeadline is how long main()'s attach probe (desktop.go) is allowed
// to keep retrying before giving up and spawning its own server (ut-docs#1199).
// Reuses ut-docs#1093's own startup-gate duration and uptime reading rather
// than introducing a second, independent notion of "how far into boot are
// we" — the two windows are the same window: waitForSafeStartup already
// holds the shell's window open until the machine is min/gateDuration()
// into boot, so retrying the attach probe across exactly that same span
// costs nothing extra once it gives up (the window was never going to open
// any sooner anyway) and, unlike a single early probe, gives the slower
// systemd-managed service a real chance to answer first.
//
// An unreadable /proc/uptime or a disabled gate (min == 0) resolves to
// "now" — decide from a single probe immediately, same as
// waitForSafeStartup's own "can't read uptime, start immediately" fallback:
// a shell that can't tell how far into boot it is should not guess by
// waiting regardless.
func attachDeadline() time.Time {
	min := gateDuration()
	if min == 0 {
		return time.Now()
	}
	up, err := readUptimeFrom(procUptime)
	if err != nil {
		return time.Now()
	}
	return time.Now().Add(holdFor(up, min))
}
