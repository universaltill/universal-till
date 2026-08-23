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
}

// NoopWindowController is the default WindowController until a real
// platform implementation is wired in (see #609/#610/#883).
type NoopWindowController struct{}

func (NoopWindowController) ExitToOS() error             { return nil }
func (NoopWindowController) ApplyMode(mode string) error { return nil }
