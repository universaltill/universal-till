package buildinfo

import "sync"

// InstallChannel is the install channel this binary was built for (ADR-0194 §2.3).
// It decides which store-policy surfaces exist: plan purchase, account link-out,
// self-update and marketplace code plugins. It is a build constant: stamped by
// the linker (desktop/Pi/Windows) or handed once by the Android shell before
// mobile.Start. It is never read from settings, config, env, a synced
// directive or request input, so a reviewed store build cannot be re-opened
// remotely.
type InstallChannel string

const (
	ChannelSideload InstallChannel = "sideload"
	ChannelPlay     InstallChannel = "play"
	ChannelAppStore InstallChannel = "appstore"
	// ChannelDirect is the website download and the Microsoft Store EXE
	// (same binary, ADR-0194 §2.6).
	ChannelDirect InstallChannel = "direct"
)

// The linker-stamped channel is the string var Channel in buildinfo.go (stamped
// on every build line, ADR-0194, #6405). An unstamped source build is "sideload".

var (
	chMu       sync.Mutex
	chSet      bool
	chExplicit bool // mobile: the AAR has no ldflags, the shell must say
	chValue    InstallChannel
)

// ParseChannel maps a raw value to a Channel. Anything that is not exactly one
// of the four names (including "") FAILS CLOSED to ChannelAppStore, the most
// restrictive channel.
func ParseChannel(v string) InstallChannel {
	switch InstallChannel(v) {
	case ChannelSideload, ChannelPlay, ChannelAppStore, ChannelDirect:
		return InstallChannel(v)
	}
	return ChannelAppStore
}

// CurrentChannel is this binary's channel.
func CurrentChannel() InstallChannel {
	chMu.Lock()
	defer chMu.Unlock()
	if chSet {
		return chValue
	}
	if chExplicit {
		return ChannelAppStore // the shell never said: fail closed
	}
	return ParseChannel(Channel)
}

// RequireExplicitChannel is called by the gomobile package at init: a mobile
// build takes its channel only from SetChannelOnce, never from the default.
func RequireExplicitChannel() {
	chMu.Lock()
	chExplicit = true
	chMu.Unlock()
}

// SetChannelOnce records the channel the Android shell passes (BuildConfig)
// before mobile.Start. The first call wins; later calls change nothing and
// return false. An unknown value is stored as ChannelAppStore.
func SetChannelOnce(v string) bool {
	chMu.Lock()
	defer chMu.Unlock()
	if chSet {
		return false
	}
	chValue, chSet = ParseChannel(v), true
	return true
}

// PlanPurchaseAllowed: plan panel, Choose-a-plan QR, purchase prompts and the
// purchase routes (which answer 404 where this is false).
func (c InstallChannel) PlanPurchaseAllowed() bool { return c == ChannelSideload || c == ChannelDirect }

// AccountLinkOutAllowed: the claim link and QR and any account link that
// leads to checkout. Where false the claim code shows as text only.
func (c InstallChannel) AccountLinkOutAllowed() bool {
	return c == ChannelSideload || c == ChannelDirect
}

// InAppUpdateAllowed: the GitHub update check and the APK install bridge.
func (c InstallChannel) InAppUpdateAllowed() bool { return c == ChannelSideload || c == ChannelDirect }

// OverrideChannelForTest swaps the channel for a test and returns the restore
// func. Test-only: no production code path calls it.
func OverrideChannelForTest(c InstallChannel) (restore func()) {
	chMu.Lock()
	prevSet, prevVal := chSet, chValue
	chSet, chValue = true, c
	chMu.Unlock()
	return func() {
		chMu.Lock()
		chSet, chValue = prevSet, prevVal
		chMu.Unlock()
	}
}
