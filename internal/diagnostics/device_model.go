package diagnostics

import "sync"

var (
	deviceModelMu  sync.RWMutex
	deviceModelVal string
)

// SetDeviceModel registers the on-device hardware model string (e.g.
// Android's Build.MODEL) — this package cannot read it on its own, since
// diagnostics runs identically across desktop/service/mobile builds.
// Called once by the mobile gomobile-bind shell (mobile.SetDeviceModel),
// before Start, mirroring internal/bluetooth's SetAndroidBridge shape.
// Passing "" clears it (desktop/service builds never call this, so it
// stays "" there — the accepted gap this replaces).
//
// Listed on scripts/ci/deadcode-baseline.txt as a known false positive of
// the whole-program guard, NOT as dead code — same reasoning as
// internal/bluetooth.SetAndroidBridge's own doc comment: its one
// production caller, mobile.SetDeviceModel, lives in the gomobile-bind
// library package `mobile`, which has no main and so is not one of
// guard-deadcode-baseline.sh's three roots, so that analysis never sees
// the call. The Kotlin shell reaches it through that binding at app start
// (TillService.kt's Mobile.setDeviceModel(Build.MODEL), ut-docs#2235).
func SetDeviceModel(model string) {
	deviceModelMu.Lock()
	defer deviceModelMu.Unlock()
	deviceModelVal = model
}

// DeviceModel reports the model SetDeviceModel last registered, "" if none.
func DeviceModel() string {
	deviceModelMu.RLock()
	defer deviceModelMu.RUnlock()
	return deviceModelVal
}
