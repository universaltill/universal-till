package androidupdate

import "sync"

// Prefetcher asks the Android shell to download and verify the update APK
// without launching the installer (ut-docs#6551). Called on a Go goroutine;
// the implementation hops to its UI thread and returns at once. A nil error
// means the request was handed to native code, not that the download is done.
//
// Like internal/kiosk's Bridge it is stored here rather than in package
// mobile because internal/pages cannot import mobile (mobile → app → pages).
// SelfUpdateActivity registers it in onResume and clears it in onPause
// (mobile.SetUpdateBridge); nil means the Activity is not in front, this is a
// Play build (which never registers it) or the process is not Android.
type Prefetcher interface {
	PrefetchUpdate() error
}

var (
	pmu        sync.RWMutex
	prefetcher Prefetcher
)

// SetPrefetcher registers the shell's prefetch bridge; nil un-registers it.
//
// Listed on scripts/ci/deadcode-baseline.txt as a known false positive of
// the whole-program guard: its only production caller, mobile.SetUpdateBridge,
// lives in the gomobile-bind library package `mobile`, which is not a root of
// guard-deadcode-baseline.sh (same as kiosk.SetBridge).
func SetPrefetcher(p Prefetcher) {
	pmu.Lock()
	defer pmu.Unlock()
	prefetcher = p
}

// RegisteredPrefetcher returns the registered bridge, nil when none. Call it
// outside pmu; the returned bridge may be invoked freely.
func RegisteredPrefetcher() Prefetcher {
	pmu.RLock()
	defer pmu.RUnlock()
	return prefetcher
}
