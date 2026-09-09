package main

import (
	"fmt"
	"os"
	"time"
)

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
// retries past that window (attachDeadline derives from the same gate
// duration as waitForSafeStartup, which was holding the window shut for
// exactly that long anyway).
//
// On the attach path that costs nothing: waitForSafeStartup still opens the
// window at the same instant it always did. One case does pay a little
// (review, ut-docs#1199) — a cold boot where the retry runs its whole
// window and still finds nothing to attach to. The unitill-pos child is
// then spawned AFTER that window rather than at the very first probe, so
// its start-up and main()'s dial-wait loop no longer overlap the gate's
// hold and the till appears a few seconds later than it used to (a tarball
// install, or a .deb whose service is down). That is inherent rather than
// an oversight: spawning speculatively in parallel with the retry is
// precisely the second-server split-brain this exists to prevent.
//
// ut-docs#1279 (review follow-up): this decision used to be entirely
// silent — a field engineer reading journal output on an affected till saw
// up to ~53s of nothing before the shell either attached or gave up and
// spawned, with no indication which branch it took or why. waitForAttach
// now logs exactly one line to stderr right before it returns, via
// attachDecisionLine, mirroring waitForSafeStartup's own single stderr
// line (startup_gate_linux.go) rather than logging per-probe and spamming
// a retry window.
func waitForAttach(deadline time.Time, probe func() bool, sleep func(time.Duration), now func() time.Time) bool {
	start := now()
	retryWindow := start.Before(deadline)
	attempts := 0
	for {
		attempts++
		if probe() {
			fmt.Fprint(os.Stderr, attachDecisionLine(retryWindow, attempts, now().Sub(start), true))
			return true
		}
		if !now().Before(deadline) {
			fmt.Fprint(os.Stderr, attachDecisionLine(retryWindow, attempts, now().Sub(start), false))
			return false
		}
		sleep(attachPollInterval)
	}
}

// attachDecisionLine renders the operator-facing stderr line waitForAttach
// logs once it has decided how to proceed (ut-docs#1279). Split out as a
// pure string builder — no clock, no I/O — so the message content is
// directly testable, same "pure logic tested, Fprintf itself isn't" split
// startup_gate.go already uses for waitForSafeStartup/holdFor.
//
// retryWindow reports whether the deadline gave the probe any chance to
// retry at all: false covers a disabled/unreadable startup gate, a
// platform with no gate (attach_gate_other.go), or a warm/manual launch
// already past the gate window — all of which decide from a single probe
// exactly as before ut-docs#1199. attempts is how many times probe() was
// called; elapsed is roughly how long the whole decision took (near-zero
// for a single immediate probe); attached is the outcome.
func attachDecisionLine(retryWindow bool, attempts int, elapsed time.Duration, attached bool) string {
	window := "no retry window (decided immediately)"
	if retryWindow {
		window = "retry window open"
	}
	outcome := "no existing server — spawning our own"
	if attached {
		outcome = "attached to an existing server"
	}
	plural := "s"
	if attempts == 1 {
		plural = ""
	}
	return fmt.Sprintf("attach gate: %s, %s after %d probe%s over %s (ut-docs#1199)\n",
		window, outcome, attempts, plural, elapsed)
}
