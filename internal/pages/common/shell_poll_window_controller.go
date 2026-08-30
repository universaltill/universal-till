package common

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/universaltill/universal-till/internal/logging"
)

// ErrNoWindowControl is ExitToOS's honest answer when no desktop shell is
// attached and no fallback channel exists — nothing can act on the window,
// and saying so beats the fabricated 204 the NoopWindowController default
// used to produce (the ut-docs#1039 field trap). errors.Is-able so the
// settings handler can map it to a 503 with an operator-facing message.
var ErrNoWindowControl = errors.New("no desktop shell is attached to control the window")

// ErrExitNotConfirmed is ExitToOS's answer when a shell was attached and
// the exit was signalled, but no applied=normal acknowledgement came back
// within ShellExitAckTimeout — the window may or may not have come back,
// and the operator must not be told "Exited to OS" on a guess.
var ErrExitNotConfirmed = errors.New("the desktop shell did not confirm leaving the window mode")

// ShellPollWindowController is the WindowController for the polled shell
// channel (ADR-0064, ut-docs#1039) — the attach-mode default that replaces
// NoopWindowController in pages.Init. It never talks to the shell
// directly: it writes the ShellChannel's live state, and the shell's own
// long poll (all traffic shell → server, never the reverse) picks the
// change up within one loopback round trip.
type ShellPollWindowController struct {
	ch *ShellChannel
	// fallback is ut-docs#882's env-handed HTTPWindowController when this
	// process was spawned by a shell (nil otherwise) — kept, per ADR-0064
	// Decision 5, as the live path for a spawn-mode shell too old to poll.
	fallback WindowController
	// ackTimeout overrides ShellExitAckTimeout in tests; zero means the
	// real constant.
	ackTimeout time.Duration
}

// NewShellPollWindowController returns the channel-backed controller.
// fallback may be nil. Constructing it is what marks ch as the exit path
// (review of ut-docs#1039, blocker 2): the fail-closed downgrade in
// GET /api/window-mode serves chrome-hiding modes only over a channel this
// controller consumes, and tying the mark to the constructor — rather than
// a flag someone must remember to set — makes "the channel is the exit
// path" true exactly when the controller that makes it true exists.
func NewShellPollWindowController(ch *ShellChannel, fallback WindowController) WindowController {
	ch.MarkExitPath()
	return &ShellPollWindowController{ch: ch, fallback: fallback}
}

// ApplyMode publishes the new live mode and never errors: persisting a
// preference while no shell is attached is legitimate (configure the till
// now, launch the shell later), and the GET /api/window-mode fail-closed
// downgrade guarantees a saved-but-unapplied chrome-hiding mode can never
// become a trap — no client that cannot leave the mode is ever served it.
// When no shell is polling but a spawn-mode fallback channel exists (an
// old shell that handed us its env-token listener), the live apply is
// forwarded there too, best-effort: a failure means the old shell simply
// applies at its next launch (ut-docs#611's original semantics), which is
// not worth failing a successfully-persisted setting over.
func (c *ShellPollWindowController) ApplyMode(mode string) error {
	c.ch.SetMode(mode)
	if c.fallback != nil && !c.ch.Attached(ShellAttachedWindow) {
		if err := c.fallback.ApplyMode(mode); err != nil {
			logging.L().Errorf("spawn-mode fallback apply %s: %v", mode, err)
		}
	}
	return nil
}

// ExitToOS sets the live mode to normal and reports the truth: nil only
// once the shell acknowledges having applied it. With no shell polling,
// it delegates to the spawn-mode fallback when one exists, else returns
// ErrNoWindowControl — never a silent success.
//
// Deliberately does NOT touch the persisted display.window_mode: exit is
// "leave lockdown now"; the configured mode still applies at next launch
// (ut-docs#549, ADR-0064 Decision 2).
func (c *ShellPollWindowController) ExitToOS() error {
	if !c.ch.Attached(ShellAttachedWindow) {
		if c.fallback != nil {
			return c.fallback.ExitToOS()
		}
		return ErrNoWindowControl
	}
	c.ch.SetMode("normal")
	timeout := c.ackTimeout
	if timeout <= 0 {
		timeout = ShellExitAckTimeout
	}
	if !c.ch.WaitApplied(context.Background(), "normal", timeout) {
		return fmt.Errorf("%w (no applied=normal acknowledgement within %s)", ErrExitNotConfirmed, timeout)
	}
	return nil
}

// RecordInputHeartbeat forwards to the spawn-mode fallback control channel
// when one exists (ut-docs#1329, split from #1228's input-freeze
// incident). Unlike ApplyMode above, this is unconditional — never gated
// on !c.ch.Attached(...) — because there is no ShellChannel field a
// heartbeat could be published into for an attach-mode shell to pick up on
// its own next long poll (ShellChannel carries window-MODE state, not an
// input-liveness timestamp); the fallback's direct loopback+token channel
// to unitill-desktop's own POST /input-heartbeat is the only path this
// diagnostic signal can travel over at all. A pure attach-mode shell (nil
// fallback — the common .deb-install topology per ADR-0064) simply has no
// live channel yet for this to reach, same as ApplyMode's own "no shell,
// no fallback" case; this is deliberately never an error either way — a
// heartbeat that can't be delivered must not disturb the caller (fired
// many times a minute from every kiosk screen), so a genuine fallback
// failure is logged, not returned. Logged at Info, deliberately NOT
// Errorf/Warnf (review of ut-docs#1329, blocker 2): logging.L()'s
// recentBuf — the capped ring behind both the backoffice "recent
// problems" panel and the ADR-0018 cloud-sync heartbeat digest — admits
// anything >= Warn, and this call can fire every ~5s per open kiosk page.
// A single unreachable/erroring shell would fill the whole 50-slot buffer
// in minutes and evict every genuine problem — exactly the regression
// ut-docs#954 fixed on 2026-08-24, and especially self-defeating here
// since a wedged/unreachable shell is precisely the incident class this
// diagnostic exists to illuminate, not obscure.
func (c *ShellPollWindowController) RecordInputHeartbeat() error {
	if c.fallback != nil {
		if err := c.fallback.RecordInputHeartbeat(); err != nil {
			logging.L().Infof("input heartbeat forward: %v", err)
		}
	}
	return nil
}
