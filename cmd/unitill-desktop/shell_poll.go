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
	"net/http"
	"net/url"
	"strconv"
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

// newShellPollClient is the client showWindow hands watchShellMode —
// plain, loopback-only traffic; the timeout is the load-bearing part.
func newShellPollClient() *http.Client {
	return &http.Client{Timeout: shellPollClientTimeout}
}

// watchShellMode long-polls GET /api/window-mode?control=live and calls
// apply(mode) on every change, forever, until ctx is cancelled — the only
// exit. `initial` is the mode the shell already applied at launch (from
// fetchShellPrefs) and `since` that fetch's revision, so a Settings change
// landing between the launch fetch and the first poll is caught by the
// first poll (the server answers immediately whenever since is stale).
//
// applied= carries the last mode apply() was invoked for — updated only
// after apply returns — which is both this shell's heartbeat (it keeps the
// server's Attached() true, and with it this shell's entitlement to
// chrome-hiding modes) and its acknowledgement (it releases the server's
// exit-to-os WaitApplied, turning the operator's "Exited to OS." into a
// statement of fact). Errors never end the loop: the server being down or
// mid-restart is routine, so back off shellPollRetryDelay and try again.
func watchShellMode(ctx context.Context, client *http.Client, baseURL string, initial string, since uint64, apply func(mode string)) {
	lastApplied := initial
	backoff := func() bool { // false = ctx cancelled, stop
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
			"applied": {lastApplied},
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
		since = body.Data.Rev
		mode := body.Data.WindowMode
		switch mode {
		case "fullscreen", "kiosk", "maximized", "normal":
		default:
			// A future mode this build predates — same degradation as
			// fetchShellPrefs: never a window that can't be escaped.
			mode = "normal"
		}
		if mode != lastApplied {
			apply(mode)
			lastApplied = mode
		}
		// A healthy answer needs no backoff — the next request parks in
		// the server's own long-poll hold, so this loop is not a busy one.
	}
}
