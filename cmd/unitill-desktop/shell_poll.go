// Shell half of the polled window-control channel (ADR-0064, ut-docs#1039)
// — deliberately free of any cgo/OS build tag, like control.go and
// window_mode.go, so `go test ./...` (which never passes `-tags desktop`,
// see stub.go) actually exercises the whole loop. The per-OS glue that
// turns apply() into real GTK calls stays in showWindow's wiring
// (webview_fallback.go).
//
// Direction is the whole point: ALL traffic is shell → server. The server
// (unitill-pos) keeps an authoritative live window mode; this loop
// long-polls it and applies what it is told. That works identically
// whether this shell spawned the server (dev/mac/tar) or attached to an
// already-running systemd service (.deb) — the topology that broke
// ut-docs#882's env-handed channel, because you cannot hand environment
// variables to a process you did not spawn. Advertising control=live on
// these polls is also what entitles this shell to be SERVED the
// chrome-hiding modes at all: the server downgrades kiosk/fullscreen to
// normal for any client not holding a live poll, so "the window is locked
// down" and "the exit works" can never diverge again.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"sync"
	"time"
)

// shellPollWaitSeconds is how long each poll asks the server to hold
// (?wait=). Must not exceed the server's own ShellPollMaxWait
// (internal/pages/common/shell_channel.go, 25s) — there is no shared
// package between the two binaries, so the number itself is the contract,
// same as control.go's env-var names. The server clamps a larger value, so
// drift degrades gracefully rather than breaking.
const shellPollWaitSeconds = 25

// shellPollClientTimeout bounds one poll round trip: the server's hold
// (up to shellPollWaitSeconds) plus generous headroom for the response
// itself. Without a timeout comfortably ABOVE the wait, every healthy
// long poll would be severed mid-hold and look like an error.
const shellPollClientTimeout = (shellPollWaitSeconds + 15) * time.Second

// shellPollRetryDelay paces retries after a failed poll (server down,
// error status, malformed body) so a dead server is probed a couple of
// times a second at most, never hot-looped. A package var, not a const,
// so tests can shrink it.
var shellPollRetryDelay = 2 * time.Second

// shellPollDowngradeAfter is the shell-side outage watchdog (review of
// ut-docs#1039, finding 5): once consecutive poll failures have lasted
// this long while the window is in a chrome-hiding mode, the shell applies
// "normal" on its own — an `apt upgrade` that leaves unitill-pos
// crash-looping must not leave a fullscreen+undecorated "can't connect"
// page on a keyboardless touchscreen with no exit. Must equal the server's
// common.ShellAttachedWindow (60s) so both sides give up on each other on
// the same clock — no shared package between the binaries, so the number
// is the contract, pinned by TestShellPollContractsMatchCommonPackage. A
// package var so tests can shrink it.
var shellPollDowngradeAfter = 60 * time.Second

// isChromeHidingMode mirrors the server's common.IsChromeHiding via this
// binary's own flag table: a mode whose window hides the OS chrome
// (undecorated fullscreen) is the only kind the outage watchdog must
// unseal.
func isChromeHidingMode(mode string) bool {
	f := flagsForWindowMode(mode)
	return f.Fullscreen && !f.Decorated
}

// newShellPollClient is the client showWindow hands watchShellMode —
// plain, loopback-only traffic; the timeout is the load-bearing part.
func newShellPollClient() *http.Client {
	return &http.Client{Timeout: shellPollClientTimeout}
}

// watchShellMode long-polls GET /api/window-mode?control=live and calls
// apply(mode, done) on every change, forever, until ctx is cancelled — the
// only exit. `initial` is the mode the shell already applied at launch
// (from fetchShellPrefs) and `since` that fetch's revision, so a Settings
// change landing between the launch fetch and the first poll is caught by
// the first poll (the server answers immediately whenever since is stale).
//
// apply must invoke done() only once the mode has REALLY been applied to
// the window — for the GTK wiring that means from inside the dispatched
// closure, after applyWindowMode returned, not when Dispatch merely queued
// it (review of ut-docs#1039, finding 4). applied= carries the last mode
// whose done() has fired, which is both this shell's heartbeat (it keeps
// the server's Attached() true, and with it this shell's entitlement to
// chrome-hiding modes) and its acknowledgement (it releases the server's
// exit-to-os WaitApplied, turning the operator's "Exited to OS." into a
// statement of fact). A wedged GTK loop therefore simply never acks, and
// the server's honest not-confirmed path fires instead of a fabricated
// success. A mode whose apply is still pending is not re-dispatched when
// the server repeats it.
//
// Errors never end the loop: the server being down or mid-restart is
// routine, so back off shellPollRetryDelay and try again — but with the
// finding-5 watchdog: once consecutive failures outlast
// shellPollDowngradeAfter while the window is chrome-hiding, apply
// ("normal") so the outage's failure mode is a normal window, never a
// sealed one.
func watchShellMode(ctx context.Context, client *http.Client, baseURL string, initial string, since uint64, apply func(mode string, done func())) {
	// lastApplied is the mode the window really reached (done() fired) —
	// guarded by mu because done runs on the GTK thread, not this loop.
	// lastRequested is the mode most recently handed to apply, loop-local.
	var mu sync.Mutex
	lastApplied := initial
	lastRequested := initial
	appliedNow := func() string {
		mu.Lock()
		defer mu.Unlock()
		return lastApplied
	}
	requestApply := func(mode string) {
		lastRequested = mode
		apply(mode, func() {
			mu.Lock()
			lastApplied = mode
			mu.Unlock()
		})
	}
	var failingSince time.Time
	backoff := func() bool { // false = ctx cancelled, stop
		if failingSince.IsZero() {
			failingSince = time.Now()
		} else if time.Since(failingSince) > shellPollDowngradeAfter && isChromeHidingMode(lastRequested) {
			fmt.Fprintf(os.Stderr, "unitill-desktop: till server unreachable for over %s while the window hides OS chrome — leaving %s for a normal window (it will be re-applied when the server is back)\n",
				shellPollDowngradeAfter, lastRequested)
			requestApply("normal")
		}
		t := time.NewTimer(shellPollRetryDelay)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
			return true
		}
	}
	for ctx.Err() == nil {
		q := url.Values{
			"control": {"live"},
			"since":   {strconv.FormatUint(since, 10)},
			"applied": {appliedNow()},
			"wait":    {strconv.Itoa(shellPollWaitSeconds)},
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/window-mode?"+q.Encode(), nil)
		if err != nil {
			// Only a malformed baseURL can land here — unrecoverable by
			// retrying, but exiting would silently drop the channel; treat
			// it like any other failure and keep the loop alive.
			if !backoff() {
				return
			}
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			if !backoff() {
				return
			}
			continue
		}
		var body struct {
			Data struct {
				WindowMode string `json:"window_mode"`
				Rev        uint64 `json:"rev"`
			} `json:"data"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK || decodeErr != nil {
			if !backoff() {
				return
			}
			continue
		}
		failingSince = time.Time{} // healthy answer — reset the watchdog
		since = body.Data.Rev
		mode := body.Data.WindowMode
		switch mode {
		case "fullscreen", "kiosk", "maximized", "normal":
		default:
			// A future mode this build predates — same degradation as
			// fetchShellPrefs: never a window that can't be escaped.
			mode = "normal"
		}
		if mode != lastRequested {
			requestApply(mode)
		}
		// A healthy answer needs no backoff — the next request parks in
		// the server's own long-poll hold, so this loop is not a busy one.
	}
}
