package buildinfo

import "sync"

// Channel is the install channel this binary was built for (ADR-0194 §2.3).
// It decides which store-policy surfaces exist: plan purchase, account link-out,
// self-update and marketplace code plugins. It is a build constant: stamped by
// the linker (desktop/Pi/Windows) or handed once by the Android shell before
// mobile.Start. It is never read from settings, config, env, a synced
// directive or request input, so a reviewed store build cannot be re-opened
// remotely.
type Channel string

const (
	ChannelSideload Channel = "sideload"
	ChannelPlay     Channel = "play"
	ChannelAppStore Channel = "appstore"
	// ChannelDirect is the website download and the Microsoft Store EXE
	// (same binary, ADR-0194 §2.6).
	ChannelDirect Channel = "direct"
)

// stamped is the linker-stamped channel:
//
//	-ldflags "-X github.com/universaltill/universal-till/internal/buildinfo.stamped=direct"
//
// An unstamped source build is "direct" (a developer or CI binary, no store).
// The goreleaser builds stamp it explicitly. The mobile package never uses
// this default (RequireExplicitChannel).
var stamped = "direct"

var (
	chMu       sync.Mutex
	chSet      bool
	chExplicit bool // mobile: the AAR has no ldflags, the shell must say
	chValue    Channel
)

// ParseChannel maps a raw value to a Channel. Anything that is not exactly one
// of the four names (including "") FAILS CLOSED to ChannelAppStore, the most
// restrictive channel.
func ParseChannel(v string) Channel {
	switch Channel(v) {
	case ChannelSideload, ChannelPlay, ChannelAppStore, ChannelDirect:
		return Channel(v)
	}
	return ChannelAppStore
}

// CurrentChannel is this binary's channel.
func CurrentChannel() Channel {
	chMu.Lock()
	defer chMu.Unlock()
	if chSet {
		return chValue
	}
	if chExplicit {
		return ChannelAppStore // the shell never said: fail closed
	}
	return ParseChannel(stamped)
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
func (c Channel) PlanPurchaseAllowed() bool { return c == ChannelSideload || c == ChannelDirect }

// AccountLinkOutAllowed: the claim link and QR and any account link that
// leads to checkout. Where false the claim code shows as text only.
func (c Channel) AccountLinkOutAllowed() bool { return c == ChannelSideload || c == ChannelDirect }

// InAppUpdateAllowed: the GitHub update check and the APK install bridge.
func (c Channel) InAppUpdateAllowed() bool { return c == ChannelSideload || c == ChannelDirect }

// OverrideChannelForTest swaps the channel for a test and returns the restore
// func. Test-only: no production code path calls it.
func OverrideChannelForTest(c Channel) (restore func()) {
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
