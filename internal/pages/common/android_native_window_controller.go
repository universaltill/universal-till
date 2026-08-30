package common

// AndroidNativeWindowController is the WindowController for the Android
// native shell (ut-docs#1254, ADR-0023). Unlike the desktop/Pi platforms,
// there is no separate OS window to hand back — the embedded Go server
// runs in-process inside the same app the operator is already looking at,
// and "leaving kiosk mode" on Android means releasing MainActivity's own
// Lock Task/screen-pinning, which only the Kotlin side
// (MainActivity.KioskBridge) can do, not this process.
//
// The real authorization already happened by the time ExitToOS is reached:
// settings_page.go's handler calls AuthorizeManager(pin) — a live,
// lockout-tracked PIN check — BEFORE it ever touches Deps.WindowCtl. So
// this controller's only job is to report the honest, audited success that
// login.html/settings.html's window.AndroidKiosk.exitLockdown() bridge
// call is gated on (both only fire on a 2xx response, never on the 503
// ShellPollWindowController would otherwise produce here with no shell to
// poll — exactly the gap this type exists to close). It never touches any
// OS-level window state, because on this platform there isn't one to
// touch — ApplyMode is a no-op for the same reason (there is no separate
// window-mode concept on the Android native shell to apply one to).
type AndroidNativeWindowController struct{}

func (AndroidNativeWindowController) ExitToOS() error             { return nil }
func (AndroidNativeWindowController) ApplyMode(mode string) error { return nil }

// InputHeartbeat is a no-op here too: there is no separate shell process on
// this platform to relay a liveness signal to (see the type doc comment) —
// ut-docs#1329's diagnosability plumbing targets the desktop-shell
// architecture where unitill-desktop is a distinct process worth reporting
// to.
func (AndroidNativeWindowController) InputHeartbeat() error { return nil }
