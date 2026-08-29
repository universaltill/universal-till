package main

import "time"

// attachPollInterval paces retries of the attach probe against a
// slow-to-bind systemd service (ut-docs#1199): small relative to the up to
// 60s startup gate (ut-docs#1093) so the shell notices the service the
// moment it binds :8080, without hot-looping the probe.
const attachPollInterval = 500 * time.Millisecond

// waitForAttach repeats probe (a health check against the would-be already-
// running server) at attachPollInterval until either it reports true — the
// shell attaches instead of spawning its own server — or deadline passes
// with no success, in which case it gives up and the caller spawns. now and
// sleep are injected (no direct clock/file I/O, same reasoning as
// startup_gate.go's holdFor) so the retry-vs-give-up decision is testable
// without real wall-clock waits.
//
// ut-docs#1199: main()'s attach-vs-spawn decision used to be a single probe
// call. On a cold .deb boot, the shell's own process starts well before the
// systemd-managed unitill-pos service has finished binding :8080 — one
// probe at, say, T+7s loses that race every time, so the shell spawns a
// second server as the desktop user instead of attaching to the real one.
// Both servers then run: the on-screen till trades against the spawned
// child's own SQLite file instead of the service's, and in-app update
// honestly (but confusingly) reports unsupported, because the *serving*
// process cannot write the service's install directory.
//
// A deadline that is not after `now()` (already passed, or equal) decides
// immediately from a single probe call — the exact behaviour this
// replaces — so a warm or manual launch, a platform with no startup gate,
// or the gate disabled outright all cost exactly the one probe they always
// cost. Only a cold boot still inside the gate window retries, and it never
// waits any longer than that window already held the window itself back
// for (attachDeadline derives from the same gate duration as
// waitForSafeStartup) — so this removes the false-early spawn decision
// without adding any new perceived delay on top of ut-docs#1093's existing
// hold.
func waitForAttach(deadline time.Time, probe func() bool, sleep func(time.Duration), now func() time.Time) bool {
	for {
		if probe() {
			return true
		}
		if !now().Before(deadline) {
			return false
		}
		sleep(attachPollInterval)
	}
}
