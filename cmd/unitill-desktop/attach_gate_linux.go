//go:build desktop && linux

package main

import "time"

// attachDeadline is how long main()'s attach probe (desktop.go) is allowed
// to keep retrying before giving up and spawning its own server (ut-docs#1199).
// When the render gate is active, it reuses ut-docs#1093's own startup-gate
// duration and uptime reading rather than introducing a second, independent
// notion of "how far into boot are we" (see attachRetryDuration in
// attach_gate.go). When the gate is disabled, attachRetryDuration still
// supplies the attach-race retry's own floor (ut-docs#1278) — reading
// /proc/uptime is skipped entirely in that case since the floor doesn't need
// it, mirroring the original disabled-gate fast path.
//
// An unreadable /proc/uptime resolves to "now" — decide from a single probe
// immediately, same as waitForSafeStartup's own "can't read uptime, start
// immediately" fallback: a shell that can't tell how far into boot it is
// should not guess by waiting regardless. (This differs from the gate's own
// disabled case, which is a deliberate operator choice, not a read failure —
// the floor still applies there.)
func attachDeadline() time.Time {
	min := gateDuration()
	if min == 0 {
		return time.Now().Add(attachRetryDuration(0, min))
	}
	up, err := readUptimeFrom(procUptime)
	if err != nil {
		return time.Now()
	}
	return time.Now().Add(attachRetryDuration(up, min))
}
