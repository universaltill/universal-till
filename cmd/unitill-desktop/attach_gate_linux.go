//go:build desktop && linux

package main

import "time"

// attachDeadline is how long main()'s attach probe (desktop.go) is allowed
// to keep retrying before giving up and spawning its own server (ut-docs#1199).
// When the render gate is active and uptime is readable, it reuses
// ut-docs#1093's own startup-gate duration and uptime reading rather than
// introducing a second, independent notion of "how far into boot are we"
// (see attachRetryDuration in attach_gate.go), with the retry floor
// (UT_SHELL_ATTACH_RETRY_SECONDS, ut-docs#2248) measured from boot too.
//
// When the gate is disabled, reading /proc/uptime is skipped entirely since
// the floor doesn't need it (ut-docs#1278). An unreadable /proc/uptime now
// gets the same treatment (ut-docs#2248): the floor is absolute from now. It
// used to resolve to "now" (a single immediate probe), but the floor needs no
// uptime knowledge, and a shell that cannot tell how far into boot it is
// should still cover the ut-docs#1199 boot race rather than lose it. This
// differs from waitForSafeStartup's "can't read uptime, start immediately"
// fallback because that gate guards a render defect and has nothing to
// measure against; the attach retry always has the floor.
func attachDeadline() time.Time {
	floor := attachRetryFloorDuration()
	min := gateDuration()
	if min == 0 {
		return time.Now().Add(attachRetryDuration(0, false, min, floor))
	}
	up, err := readUptimeFrom(procUptime)
	return time.Now().Add(attachRetryDuration(up, err == nil, min, floor))
}
