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
// fallback may be nil.
func NewShellPollWindowController(ch *ShellChannel, fallback WindowController) WindowController {
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
