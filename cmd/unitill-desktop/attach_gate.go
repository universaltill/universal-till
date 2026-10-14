package main

import (
	"fmt"
	"time"

	"github.com/universaltill/universal-till/internal/logging"
)

// attachPollInterval paces retries of the attach probe against a
// slow-to-bind systemd service (ut-docs#1199): small relative to the up to
// 60s startup gate (ut-docs#1093) so the shell notices the service the
// moment it binds :8080, without hot-looping the probe.
const attachPollInterval = 500 * time.Millisecond

// defaultAttachRetryFloor is the attach-probe retry's own minimum, held open
// regardless of UT_SHELL_MIN_UPTIME_SECONDS (ut-docs#1278, review follow-up
// to ut-docs#1199). Before this, attachDeadline() (attach_gate_linux.go)
// derived the whole retry window from the render-defect startup gate's own
// duration, so an operator disabling that gate for a reason unrelated to
// ut-docs#1199 — X11 instead of Wayland, a non-Pi Linux desktop, any machine
// without the WebKitGTK 2.52.6 compositing defect — silently also disabled
// the boot-race retry: that machine reverted to a single attach probe and
// could still lose the race #1199 fixes. The render-defect gate and the
// attach-race retry each need their own on/off switch. 15s is a recommended
// default (per the review's suggestion); ut-docs#2248 made it tunable via
// attachRetryEnv for deployments whose systemd unit starts slower.
const defaultAttachRetryFloor = 15 * time.Second

// attachRetryEnv lets an operator tune or disable the attach-retry floor
// (ut-docs#2248). "0" disables it; parsing, the 600s cap and the
// fallback-with-stderr-line are shared with UT_SHELL_MIN_UPTIME_SECONDS via
// secondsFromEnv (startup_gate.go).
const attachRetryEnv = "UT_SHELL_ATTACH_RETRY_SECONDS"

// attachRetryDuration is attachDeadline's pure decision (attach_gate_linux.go)
// of how long from now to keep retrying the attach probe, given the render
// gate's resolved minimum (min, from gateDuration()), the retry floor (floor,
// from attachRetryFloorDuration()) and, when readable, how long the machine
// has been up (up; upKnown is false when /proc/uptime could not be read or
// was not consulted). Split out as pure logic — no clock, no file I/O — so
// the decision is directly testable here under the plain (non-desktop-tagged)
// build, same "test the pure logic, leave time.Now()/file I/O untested" split
// startup_gate.go's holdFor already uses for waitForSafeStartup. It lives in
// this untagged file, not the desktop&&linux-only attach_gate_linux.go,
// specifically so its test runs in the CI job that actually executes
// `go test` on this package — the desktop-shell job builds and vets
// attach_gate_linux.go but never tests it.
//
// min == 0 means the render-defect gate is explicitly disabled
// (UT_SHELL_MIN_UPTIME_SECONDS=0), and !upKnown means uptime is unreadable.
// In both cases the attach-race retry is a separate concern that needs no
// uptime knowledge, so it gets the absolute floor from now rather than
// collapsing to a single immediate probe (ut-docs#1278; the unreadable case
// ut-docs#2248). Otherwise the window is holdFor(up, max(min, floor)): the
// floor is measured from boot exactly like the gate, so a small non-zero gate
// (e.g. 1s, where holdFor(7s, 1s) would be 0) cannot reopen the ut-docs#1199
// race, while a warm/manual launch already past both still costs exactly one
// probe. With the default 60s gate max(60s, 15s) = 60s, so the two windows
// still coincide, deliberately — retrying across the span the render gate is
// already holding open costs nothing extra. A floor of 0 (operator-disabled)
// leaves plain holdFor(up, min); with the gate also disabled that is the
// pre-#1278 single immediate probe.
func attachRetryDuration(up time.Duration, upKnown bool, min, floor time.Duration) time.Duration {
	if min == 0 || !upKnown {
		return floor
	}
	return holdFor(up, max(min, floor))
}

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
// replaces — so a warm or manual launch, or a platform with no startup gate
// at all, cost exactly the one probe they always cost. Only a cold boot
// still inside a retry window retries, and it never retries past that
// window: on Linux with the render-defect gate active, that window runs
// until uptime reaches max(gate, floor) — with the default 60s gate, the
// same span waitForSafeStartup is already holding the window shut for (see
// below); with the gate disabled (ut-docs#1278) or /proc/uptime unreadable
// (ut-docs#2248), it's attachRetryDuration's own floor from launch instead
// (tunable via UT_SHELL_ATTACH_RETRY_SECONDS) — neither collapses this to a
// single immediate probe the way it used to.
//
// On the attach path that costs nothing: waitForSafeStartup still opens the
// window at the same instant it always did. One case does pay a little
// (review, ut-docs#1199) — a cold boot where the retry runs its whole
// window and still finds nothing to attach to. The unitill-pos child is
// then spawned AFTER that window rather than at the very first probe, so
// its start-up and main()'s dial-wait loop no longer overlap the gate's
// hold and the till appears a few seconds later than it used to (a tarball
// install, or a .deb whose service is down). The same delay, up to the
// floor and with no gate hold to overlap, applies with the gate disabled,
// uptime unreadable, or a gate shorter than the floor (ut-docs#2248).
// That is inherent rather than
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
			fmt.Fprint(logging.Stderr(), attachDecisionLine(retryWindow, attempts, now().Sub(start), true))
			return true
		}
		if !now().Before(deadline) {
			fmt.Fprint(logging.Stderr(), attachDecisionLine(retryWindow, attempts, now().Sub(start), false))
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
// retry at all: false covers both switches at 0 (UT_SHELL_MIN_UPTIME_SECONDS
// and UT_SHELL_ATTACH_RETRY_SECONDS), a platform with no gate
// (attach_gate_other.go), or a launch already past max(gate, floor) — all of
// which decide from a single probe exactly as before ut-docs#1199. A disabled
// gate or unreadable uptime still opens a window (the floor; ut-docs#1278,
// ut-docs#2248). attempts is how many times probe() was
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
