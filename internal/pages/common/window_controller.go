package common

// WindowController is the host-OS hook for the till's own window/process —
// exiting kiosk/fullscreen to the OS desktop, and applying a window-mode
// change. This card (ut-docs#608) built the interface and a no-op stub;
// #883 (Pi headless kiosk) wires KioskSystemdWindowController below, and
// #882 wires HTTPWindowController for the desktop-shell platforms (talks to
// unitill-desktop's own cross-process control channel) — real end-to-end on
// Linux, present but inert on macOS/Windows (#609/#610 still owe the native
// handler; a live call there is accepted and simply has no visible effect
// until next launch, see settings_page.go's window-mode handler comment).
type WindowController interface {
	// ExitToOS leaves kiosk/fullscreen mode and returns control to the OS
	// desktop. The scaffold's NoopWindowController is a no-op until a
	// platform-specific implementation lands.
	ExitToOS() error

	// ApplyMode is called whenever the persisted window-mode setting
	// changes (ut-docs#883). NoopWindowController ignores it; a real
	// implementation applies the new mode to the actual OS window/service.
	ApplyMode(mode string) error

	// InputHeartbeat relays a kiosk page's own input-liveness signal
	// (ut-docs#1329, split from #1228's Pi5-1 input-freeze incident) to
	// whatever live shell control channel this implementation has,
	// best-effort. Unlike ExitToOS/ApplyMode this is never PIN-gated and
	// never audited — it is a high-frequency passive diagnostic signal
	// fired from ordinary page JS, not an operator action. An
	// implementation with no live channel to relay it to (no shell
	// process exists to ask, e.g. the Pi headless kiosk appliance) is a
	// harmless no-op, same convention as ExitToOS/ApplyMode's own no-ops.
	InputHeartbeat() error
}

// NoopWindowController is a do-nothing WindowController kept for bare-Deps
// tests and the handlers' nil-WindowCtl fallback. It is deliberately NO
// LONGER the production default (ADR-0064, ut-docs#1039): pages.Init wires
// ShellPollWindowController instead, because a silent no-op behind the
// PIN-gated exit-to-os is exactly what let a .deb install engage kiosk
// mode while telling the operator "Exited to OS." over a dead channel.
type NoopWindowController struct{}

func (NoopWindowController) ExitToOS() error             { return nil }
func (NoopWindowController) ApplyMode(mode string) error { return nil }
func (NoopWindowController) InputHeartbeat() error       { return nil }
