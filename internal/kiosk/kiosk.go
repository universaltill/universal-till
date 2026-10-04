// Package kiosk holds the native self-order kiosk state the Go side cannot
// observe on its own (ut-docs#3466, ADR-0142 D3/D5): on Android, Lock Task
// (screen pinning) is Activity state that only Kotlin can read or change.
//
// Two one-field stores, the shape of internal/diagnostics' device_model.go:
//
//   - KioskPinned: the pin state MainActivity intends to be in, pushed by
//     native code on every engage and release (mobile.SetKioskPinned), and
//     reported on check-in as DeviceExtra's kiosk_pinned.
//   - Bridge: the Go→native callback that releases the pin, registered by
//     MainActivity in onResume and cleared in onPause (mobile.SetKioskBridge).
//     AndroidNativeWindowController.ReleaseKiosk calls it; nil means the
//     Activity is not in the foreground (result reason no_shell).
//
// The bridge is stored here rather than in package mobile because
// internal/pages/common (which must reach it) cannot import mobile — mobile
// imports internal/app, which imports pages. mobile.KioskBridge is the
// gobind-visible mirror of Bridge (gobind binds only types declared in
// ./mobile), kept structurally identical so mobile.SetKioskBridge passes
// its argument straight through, as mobile.SetDiscoveryBridge does.
//
// Desktop, service and Pi builds never call either setter: KioskPinned
// stays false and no bridge is registered there.
package kiosk

import "sync"

// Bridge releases the native self-order kiosk pin. Called on a Go
// goroutine; the implementation switches to its own UI thread. A nil
// error means the release was handed to native code.
type Bridge interface {
	ReleaseKiosk() error
}

var (
	mu     sync.RWMutex
	pinned bool
	bridge Bridge
)

// SetKioskPinned records the pin state native code now intends (true after
// engaging Lock Task, false after releasing it).
//
// Listed on scripts/ci/deadcode-baseline.txt as a known false positive of
// the whole-program guard, NOT as dead code — same reasoning as
// internal/diagnostics.SetDeviceModel: its one production caller,
// mobile.SetKioskPinned, lives in the gomobile-bind library package
// `mobile`, which has no main and so is not one of
// guard-deadcode-baseline.sh's roots. MainActivity.kt reaches it through
// that binding.
func SetKioskPinned(p bool) {
	mu.Lock()
	defer mu.Unlock()
	pinned = p
}

// KioskPinned reports the state SetKioskPinned last recorded, false if
// none.
func KioskPinned() bool {
	mu.RLock()
	defer mu.RUnlock()
	return pinned
}

// SetBridge registers the native release callback; nil un-registers it.
//
// Listed on scripts/ci/deadcode-baseline.txt for the same reason as
// SetKioskPinned: its one production caller is mobile.SetKioskBridge.
func SetBridge(b Bridge) {
	mu.Lock()
	defer mu.Unlock()
	bridge = b
}

// RegisteredBridge reports the bridge SetBridge last registered, or nil.
// Callers invoke the bridge after this returns, never under mu: the native
// release itself calls back into SetKioskPinned (on its UI thread, while
// the Go caller waits), so holding mu across the call would deadlock.
func RegisteredBridge() Bridge {
	mu.RLock()
	defer mu.RUnlock()
	return bridge
}
