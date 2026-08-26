package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// defaultMinUptime is how far into a boot the shell refuses to create its
// window. 60s, chosen from measurement rather than taste — see
// waitForSafeStartup in startup_gate_linux.go.
const defaultMinUptime = 60 * time.Second

// minUptimeEnv lets an operator tune or disable the gate. "0" disables it.
const minUptimeEnv = "UT_SHELL_MIN_UPTIME_SECONDS"

// maxGateSeconds sanity-caps an operator-supplied UT_SHELL_MIN_UPTIME_SECONDS
// (independent review, ut-docs#1093): without a ceiling, a plausible
// seconds/milliseconds typo like "60000" holds the window for 16h40m instead
// of 60s, and a large-enough value (>= 2^63/time.Second, ~9.22e9) overflows
// the time.Duration multiplication in gateDuration into a NEGATIVE duration —
// which then silently disables the gate entirely (up >= a negative min is
// always true), exactly the "typo must not silently reintroduce the white
// screen" failure this const's sibling comment already promises never
// happens. 600s (10 minutes) is generous headroom over the measured 60s
// requirement while staying far below either failure threshold.
const maxGateSeconds = 600

// gateDuration resolves the hold from the environment, falling back to the
// default. A malformed, negative, or too-large value falls back rather than
// disabling the gate or overflowing: a typo must not silently reintroduce the
// white screen. Only an explicit "0" disables it.
func gateDuration() time.Duration {
	raw, ok := os.LookupEnv(minUptimeEnv)
	if !ok {
		return defaultMinUptime
	}
	secs, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || secs < 0 {
		fmt.Fprintf(os.Stderr, "%s=%q is not a non-negative integer; using default %s\n",
			minUptimeEnv, raw, defaultMinUptime)
		return defaultMinUptime
	}
	if secs == 0 {
		return 0
	}
	if secs > maxGateSeconds {
		fmt.Fprintf(os.Stderr, "%s=%d exceeds the %ds sanity cap; using default %s\n",
			minUptimeEnv, secs, maxGateSeconds, defaultMinUptime)
		return defaultMinUptime
	}
	return time.Duration(secs) * time.Second
}

// readUptimeFrom returns how long the machine has been up, from a /proc/uptime
// formatted file.
func readUptimeFrom(path string) (time.Duration, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	first, _, ok := strings.Cut(strings.TrimSpace(string(raw)), " ")
	if !ok {
		first = strings.TrimSpace(string(raw))
	}
	secs, err := strconv.ParseFloat(first, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %q: %w", first, err)
	}
	return time.Duration(secs * float64(time.Second)), nil
}

// holdFor returns how long waitForSafeStartup should still sleep, given the
// machine has been up for `up` and the gate wants `min` — 0 once up already
// meets or exceeds min. Split out as pure logic (no clock, no file I/O) so
// the compare-and-sleep decision is directly testable.
func holdFor(up, min time.Duration) time.Duration {
	if up >= min {
		return 0
	}
	return min - up
}
