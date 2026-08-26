//go:build desktop && linux

package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// defaultMinUptime is how far into a boot the shell refuses to create its
// window. 60s, chosen from measurement rather than taste — see waitForSafeStartup.
const defaultMinUptime = 60 * time.Second

// minUptimeEnv lets an operator tune or disable the gate. "0" disables it.
const minUptimeEnv = "UT_SHELL_MIN_UPTIME_SECONDS"

// waitForSafeStartup holds the shell back until the machine is far enough
// into its boot for WebKitGTK to render correctly (ut-docs#1093).
//
// THIS IS A MITIGATION, NOT A CURE. The underlying defect is in WebKitGTK
// 2.52.6 on Wayland/vc4: a window created early in a boot comes up with a
// corrupt compositing surface — the till paints as white with horizontal
// scanline streaks — and never recovers for the life of the process. On a
// kiosk till that autostarts at login, that is an unescapable blank screen
// with no chrome to click and no UI to type a PIN into.
//
// Measured on a Pi 5 (labwc, vc4, WebKitGTK 2.52.6, 1920x1200), classifying
// each boot's first frame against a known-good reference:
//
//	launch at T+7s  (autologin) : CORRUPT
//	launch at T+20s             : CORRUPT
//	launch at T+60s             : CLEAN  (3/3)
//	launch at T+120s            : CLEAN
//
// Things that were measured and do NOT fix it, so nobody repeats them:
// forcing a repaint (the compositor's own buffer is wrong, captured
// directly); gtk_window_resize (a no-op on a fullscreen window — verified by
// instrumenting the binary); unfullscreen/refullscreen; location.reload();
// hiding and re-showing the web view to drop its Wayland surface; and
// WEBKIT_DISABLE_DMABUF_RENDERER=1. A plain Chromium window in the same
// early slot renders perfectly, which is what scopes this to WebKitGTK
// rather than the compositor or the driver.
//
// The cost is real and deliberate: on a cold boot the till appears up to a
// minute later than it otherwise would. That is a poor trade against a fast
// boot and a good one against a white screen a shop owner cannot escape.
// Only a cold boot pays it — launched by hand on a running machine, uptime is
// already past the threshold and this returns immediately.
func waitForSafeStartup() {
	min := gateDuration()
	if min == 0 {
		return
	}

	up, err := readUptimeFrom(procUptime)
	if err != nil {
		// Unreadable /proc/uptime is not a reason to refuse to start: a till
		// that opens late is bad, one that never opens is worse.
		fmt.Fprintln(os.Stderr, "startup gate: cannot read uptime, starting immediately:", err)
		return
	}
	if up >= min {
		return
	}

	wait := min - up
	fmt.Fprintf(os.Stderr, "startup gate: %s into boot, holding %s before opening the window (ut-docs#1093)\n",
		up.Round(time.Second), wait.Round(time.Second))
	time.Sleep(wait)
}

// procUptime is the real source; tests point readUptimeFrom at a fixture.
const procUptime = "/proc/uptime"

// gateDuration resolves the hold from the environment, falling back to the
// default. A malformed or negative value falls back rather than disabling the
// gate: a typo must not silently reintroduce the white screen. Only an
// explicit "0" disables it.
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
