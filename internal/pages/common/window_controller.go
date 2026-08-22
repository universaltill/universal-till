package common

// WindowController is the host-OS hook for the till's own window/process —
// exiting kiosk/fullscreen to the OS desktop, and applying a window-mode
// change. This card (ut-docs#608) built the interface and a no-op stub;
// #609 (macOS), #610 (Windows) still owe real implementations, #611 (Linux
// desktop shell) applies its mode at its own next launch (no live channel
// yet — that's ut-docs#882), and #883 (Pi headless kiosk) wires
// KioskSystemdWindowController below.
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
