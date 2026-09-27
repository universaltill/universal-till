package main

import (
	"net/url"
	"strings"
	"sync"
	"time"
)

// Pure policy behind the Linux shell's WebKit crash/load-failure recovery
// (ut-docs#2991, AC2) — untagged so plain `go test ./...` covers it;
// webkit_recovery_linux.go is only the cgo wiring onto WebKitGTK's
// web-process-terminated / load-failed / load-changed signals. Field report:
// a Pi 5 shell sat on WebKit's built-in "WebKit encountered an internal
// error" page — a dead end on a till with no browser chrome — until someone
// restarted it. The shell now reloads the till itself instead, rate-bounded.
// (Our own recovery page with Reload / Return to desktop is a follow-up.)

// WebKitGTK error domains/codes a main-frame load-failed must NOT be treated
// as a failure for: they are the view doing what it was asked, not the till
// being unreachable. Values from webkit2gtk-4.1's WebKitError.h (checked
// against 2.52.6); the domain is the GQuark's string (g_quark_to_string).
const (
	webkitNetworkErrorDomain = "WebKitNetworkError"
	webkitPolicyErrorDomain  = "WebKitPolicyError"
	webkitPluginErrorDomain  = "WebKitPluginError"

	webkitNetworkErrorCancelled          = 302 // WEBKIT_NETWORK_ERROR_CANCELLED: a newer navigation replaced this one
	webkitPolicyErrorInterruptedByPolicy = 102 // WEBKIT_POLICY_ERROR_FRAME_LOAD_INTERRUPTED_BY_POLICY_CHANGE: e.g. a download
	webkitPluginErrorWillHandleLoad      = 204 // WEBKIT_PLUGIN_ERROR_WILL_HANDLE_LOAD
	recoveryBackoffFirstRetry            = time.Second
	recoveryBackoffMax                   = 30 * time.Second
)

// shouldRecoverLoadFailure reports whether a main-frame load-failed for
// failingURI (with GError domain/code) should be answered by reloading the
// till: only loads on the till's own origin (scheme+host+port of baseURL —
// never some external page an operator followed), and never a cancellation
// or policy interruption.
func shouldRecoverLoadFailure(baseURL, failingURI, domain string, code int) bool {
	switch {
	case domain == webkitNetworkErrorDomain && code == webkitNetworkErrorCancelled,
		domain == webkitPolicyErrorDomain && code == webkitPolicyErrorInterruptedByPolicy,
		domain == webkitPluginErrorDomain && code == webkitPluginErrorWillHandleLoad:
		return false
	}
	return sameOrigin(baseURL, failingURI)
}

// recoveryTarget is the URI to reload after the web process died: the
// page the view was on, when that is still the till's own origin, else the
// till's base URL (empty, about:blank, or anything off-origin).
func recoveryTarget(baseURL, current string) string {
	if sameOrigin(baseURL, current) {
		return current
	}
	return baseURL
}

func sameOrigin(a, b string) bool {
	ua, err := url.Parse(a)
	if err != nil || ua.Host == "" {
		return false
	}
	ub, err := url.Parse(b)
	if err != nil || ub.Host == "" {
		return false
	}
	return ua.Scheme == ub.Scheme &&
		strings.EqualFold(ua.Hostname(), ub.Hostname()) &&
		effectivePort(ua) == effectivePort(ub)
}

func effectivePort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	switch u.Scheme {
	case "http":
		return "80"
	case "https":
		return "443"
	}
	return ""
}

// recoveryBackoff spaces consecutive reload attempts: the first is
// immediate (a one-off web-process crash heals at once), then 1s, 2s, 4s …
// capped at recoveryBackoffMax. It never gives up — a till whose server is
// down for minutes (a self-update restart, a slow service start) must heal
// by itself once it returns — so it is rate-bounded, never a busy loop.
// reset() starts the sequence over once the till has stayed up for
// recoveryStableAfter after a commit (reloadScheduler.request).
type recoveryBackoff struct {
	attempts int
}

func (b *recoveryBackoff) next() time.Duration {
	b.attempts++
	if b.attempts == 1 {
		return 0
	}
	d := recoveryBackoffFirstRetry
	for i := 2; i < b.attempts && d < recoveryBackoffMax; i++ {
		d *= 2
	}
	if d > recoveryBackoffMax {
		d = recoveryBackoffMax
	}
	return d
}

func (b *recoveryBackoff) reset() { b.attempts = 0 }

// recoveryStableAfter is how long a committed page must stay up before the
// backoff starts over. A reset on the commit itself would let a web
// process that dies right after every commit (the post-upgrade library
// mismatch this card is about, or a page that OOMs it) reload at 0s
// forever (review of ut-docs#2991, MAJOR 1).
const recoveryStableAfter = 30 * time.Second

// reloadScheduler owns the recovery's timing and lifetime rules, apart
// from any GTK call: at most one reload is pending at a time (a crash
// usually raises web-process-terminated AND a load-failed — collapse
// them), each new attempt is backed off, a commit that stays up for
// recoveryStableAfter resets the backoff, and
// after close() nothing further runs — showWindow closes it before
// w.Destroy() so no timer can Dispatch onto a freed webview (same
// use-after-free reasoning as ctl.Close's ordering, ut-docs#882 review m1).
//
// run is called from the timer's goroutine while s.mu is held, so close()
// returning guarantees no run is in flight or will start; run must
// therefore only hand off (w.Dispatch) and never block or call back in.
type reloadScheduler struct {
	after func(time.Duration, func())
	now   func() time.Time

	mu         sync.Mutex
	backoff    recoveryBackoff
	lastCommit time.Time
	pending    bool
	gen        uint64 // bumped by committed(): a reload armed before it is stale
	closed     bool
}

func newReloadScheduler(after func(time.Duration, func()), now func() time.Time) *reloadScheduler {
	return &reloadScheduler{after: after, now: now}
}

// request schedules run after the next backoff delay, returning that delay
// and true — or false when one is already pending or s is closed.
func (s *reloadScheduler) request(run func()) (time.Duration, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.pending {
		return 0, false
	}
	if !s.lastCommit.IsZero() && s.now().Sub(s.lastCommit) >= recoveryStableAfter {
		s.backoff.reset()
	}
	s.pending = true
	gen := s.gen
	d := s.backoff.next()
	s.after(d, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.closed || gen != s.gen {
			return
		}
		s.pending = false
		run()
	})
	return d, true
}

// committed records a successful main-frame commit: a reload still armed
// from before the commit is dropped rather than navigating away from the
// page that just loaded, and the commit time is noted so request() can
// start the backoff over once the page has stayed up (recoveryStableAfter).
func (s *reloadScheduler) committed() {
	s.mu.Lock()
	s.lastCommit = s.now()
	s.gen++
	s.pending = false
	s.mu.Unlock()
}

// isClosed lets a dispatched closure re-check on the UI thread.
func (s *reloadScheduler) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *reloadScheduler) close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
}
