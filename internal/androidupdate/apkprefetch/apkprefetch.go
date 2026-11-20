// Package apkprefetch holds the Android shell's nightly APK prefetch bridge
// (ut-docs#6551).
package apkprefetch

import "sync/atomic"

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

// Current holds the shell's prefetch bridge; a nil pointer means none is
// registered. mobile.SetUpdateBridge stores into it directly: it is a
// variable rather than a setter func because the setter's only production
// caller would sit in the gomobile-bind package `mobile`, which is not a
// root of guard-deadcode-baseline.sh, and a feature PR may not grow that
// baseline (guard-deadcode-baseline-growth.sh, ut-docs#3406). For the same
// reason this is its own package, not part of internal/androidupdate:
// internal/pages imports it, and importing androidupdate would load that
// package's gobind-only Report* funcs into the whole-program analysis.
var Current atomic.Pointer[Prefetcher]

// RegisteredPrefetcher returns the registered bridge, nil when none. The
// returned bridge may be invoked freely.
func RegisteredPrefetcher() Prefetcher {
	if p := Current.Load(); p != nil {
		return *p
	}
	return nil
}
