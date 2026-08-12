package common

// WindowController is the host-OS hook for the till's own window/process —
// exiting kiosk/fullscreen to the OS desktop, and (later) actually applying
// a window-mode change. This card (ut-docs#608) only builds the interface
// and a no-op stub; #609 (macOS), #610 (Windows), #611 (Linux/Pi) wire real
// implementations per platform.
type WindowController interface {
	// ExitToOS leaves kiosk/fullscreen mode and returns control to the OS
	// desktop. The scaffold's NoopWindowController is a no-op until a
	// platform-specific implementation lands.
	ExitToOS() error
}

// NoopWindowController is the default WindowController until a real
// platform implementation is wired in (see #609/#610/#611).
type NoopWindowController struct{}

func (NoopWindowController) ExitToOS() error { return nil }
