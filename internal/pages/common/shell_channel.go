package common

import (
	"context"
	"sync"
	"time"
)

// Shell long-poll channel constants (ADR-0064, ut-docs#1039). There is no
// shared package between unitill-pos and unitill-desktop, so the shell's
// own copy of the poll wait (cmd/unitill-desktop/shell_poll.go) must stay
// in agreement with ShellPollMaxWait by convention — same cross-binary
// contract as EnvDesktopControlAddr above.
const (
	// ShellPollMaxWait is the longest GET /api/window-mode?control=live
	// will hold a long poll before answering with the unchanged state.
	// 25s: long enough that a healthy shell makes ~2 requests a minute
	// (negligible loopback traffic), short enough to sit comfortably under
	// common HTTP client/infrastructure 30s timeouts.
	ShellPollMaxWait = 25 * time.Second

	// ShellAttachedWindow is how recently a control poll must have been
	// seen for the shell to count as attached. Deliberately well over
	// TWICE ShellPollMaxWait: a healthy shell parked in a max-length long
	// poll checks in every ~25s, so 60s tolerates one entire missed cycle
	// (a slow reconnect, a GC pause) before the server concludes the shell
	// is gone and exit-to-os starts reporting honestly that nothing can
	// act.
	ShellAttachedWindow = 60 * time.Second

	// ShellExitAckTimeout is how long POST /api/settings/exit-to-os waits
	// for the shell's applied=normal acknowledgement before reporting
	// failure. 3s: an attached shell is parked in a long poll that returns
	// the moment the mode changes, so the real ack is one loopback round
	// trip plus one GTK dispatch — comfortably under a second; matching
	// httpWindowControllerTimeout's ceiling for the same "don't pin the
	// Settings handler on a wedged shell" reasoning.
	ShellExitAckTimeout = 3 * time.Second
)

// ShellChannel is the server-authoritative live window-mode state the
// desktop shell long-polls (ADR-0064, ut-docs#1039). It is the one channel
// over which BOTH facts travel — "which mode should the window be in" and
// "is anything actually holding the window" — which is what makes the
// fail-closed guarantee structural: chrome-hiding modes are only ever
// served to a client that is demonstrably polling this channel, so a
// locked-down window and a working exit are the same fact and cannot
// diverge.
//
// The live mode is deliberately idempotent STATE, never a queued command:
// a queue can be consumed by whoever reads first, so any other local
// process polling the endpoint could swallow the operator's exit — a state
// read cannot be stolen (ADR-0064 Decision 2).
//
// Safe for concurrent use.
type ShellChannel struct {
	mu sync.Mutex
	// mode is the live window mode — what the shell should be right now.
	// Initialised from the persisted preference; NOT written back to it
	// (exit-to-os is "leave lockdown now", the persisted preference still
	// applies at next launch, per ut-docs#549).
	mode string
	// rev increments on every real mode change; a client that polls with
	// the last rev it saw can never miss a change that happened between
	// two of its requests.
	rev uint64
	// changed is closed and replaced under mu on every state change (mode
	// or lastApplied) — the standard Go broadcast idiom: writers never
	// block on a slow waiter, waiters never busy-poll.
	changed chan struct{}
	// lastSeen is when a control=live poll last arrived; Attached derives
	// from it.
	lastSeen time.Time
	// lastApplied is the mode the shell last reported actually applying
	// (the applied= query param). Compared verbatim — an empty or garbage
	// value simply never satisfies WaitApplied; it is never clamped, so a
	// shell that has not applied anything yet ("") cannot be mistaken for
	// one that applied "normal".
	lastApplied string
	// now is injectable so staleness tests are deterministic and sleepless.
	now func() time.Time
}

// NewShellChannel returns a channel whose live mode starts at the clamped
// initial value (normally the persisted display.window_mode), rev 1.
func NewShellChannel(initial string) *ShellChannel {
	return &ShellChannel{
		mode:    ClampWindowMode(initial),
		rev:     1,
		changed: make(chan struct{}),
		now:     time.Now,
	}
}

// Snapshot returns the current live mode and revision.
func (c *ShellChannel) Snapshot() (string, uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mode, c.rev
}

// SetMode sets the live mode (clamped). A no-op — no rev bump, no
// broadcast — when the clamped mode is unchanged, so a repeated save of
// the same setting never wakes the shell for nothing.
func (c *ShellChannel) SetMode(mode string) {
	mode = ClampWindowMode(mode)
	c.mu.Lock()
	defer c.mu.Unlock()
	if mode == c.mode {
		return
	}
	c.mode = mode
	c.rev++
	c.broadcastLocked()
}

// broadcastLocked wakes every parked waiter. Callers hold c.mu.
func (c *ShellChannel) broadcastLocked() {
	close(c.changed)
	c.changed = make(chan struct{})
}

// Wait returns the current (mode, rev) as soon as rev differs from since —
// immediately when it already does (including since == 0, a first call),
// otherwise when the mode changes, ctx is cancelled, or d elapses
// (whichever comes first; the last two return the unchanged state).
func (c *ShellChannel) Wait(ctx context.Context, since uint64, d time.Duration) (string, uint64) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		c.mu.Lock()
		if c.rev != since {
			mode, rev := c.mode, c.rev
			c.mu.Unlock()
			return mode, rev
		}
		ch := c.changed
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return c.Snapshot()
		case <-timer.C:
			return c.Snapshot()
		case <-ch:
			// state changed — loop and re-read
		}
	}
}

// NoteSeen records that a live control poll arrived now, carrying the mode
// the shell last actually applied.
func (c *ShellChannel) NoteSeen(applied string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastSeen = c.now()
	if applied != c.lastApplied {
		c.lastApplied = applied
		c.broadcastLocked()
	}
}

// Attached reports whether a live control poll was seen within the last
// `within` (normally ShellAttachedWindow).
func (c *ShellChannel) Attached(within time.Duration) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.lastSeen.IsZero() && c.now().Sub(c.lastSeen) <= within
}

// WaitApplied blocks until the shell has acknowledged applying mode
// (NoteSeen with that exact value), returning true — or false when ctx is
// cancelled or d elapses first.
func (c *ShellChannel) WaitApplied(ctx context.Context, mode string, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		c.mu.Lock()
		if c.lastApplied == mode {
			c.mu.Unlock()
			return true
		}
		ch := c.changed
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
			return false
		case <-ch:
			// state changed — loop and re-read
		}
	}
}
