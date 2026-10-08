package httpx

import (
	"net/http"
	"testing"

	"github.com/universaltill/universal-till/internal/uislot"
)

// TestSnapshotState_RestoresEveryPublishedGlobal pins ut-docs#3822: a test
// in another package that drives a settings/setup handler changes these
// process-wide values, and the next test in a shuffled run inherited them
// (lang="tr", ui-scale 1.5, a key-less translator, ...). SnapshotStateForTests'
// restore must put every one of them back, and stay reusable.
func TestSnapshotState_RestoresEveryPublishedGlobal(t *testing.T) {
	// Run against a known baseline, and leave the package as it was.
	outer := SnapshotStateForTests()
	t.Cleanup(outer)
	InitI18n(nil, "en")
	InitCurrency("GBP")
	InitUIScale(1.0)
	InitOSKMode("auto")
	InitEffectsLevel("full")
	InitOrderTypePromptMode("top")
	InitIdleLock(0)
	SetLocaleGeneration(0)
	InitKiosk(false)
	InitSelfOrderMode(false)
	InitDisplayMode("")
	InitTheme("")
	InitRailAmendments(nil)
	InitRailVisibility(nil)

	baseline := SnapshotStateForTests()
	wantTV := TranslationsVersion()
	cross, bridge, avail := CrossDeviceLinkActionable, UpdateInstallBridge, UpdateAvailable

	for round := 0; round < 2; round++ { // restore is reusable
		InitI18n(nil, "tr")
		SetDefaultLocale("ar")
		InitCurrency("EUR")
		InitUIScale(1.5)
		InitOSKMode("on")
		InitEffectsLevel("light")
		InitOrderTypePromptMode("at_pay")
		InitIdleLock(5)
		SetLocaleGeneration(7)
		InitKiosk(true)
		InitSelfOrderMode(true)
		InitDisplayMode("self_order")
		InitTheme("dark")
		InitRailAmendments(func() []uislot.Amendment { return nil })
		InitRailVisibility(func(*http.Request, string) bool { return false })
		// The opposite of the baseline, so a dropped restore fails on any GOOS.
		CrossDeviceLinkActionable = func() bool { return !cross() }
		UpdateInstallBridge = func() bool { return !bridge() }
		UpdateAvailable = func() bool { return !avail() }

		baseline()

		if got := TranslationsVersion(); got != wantTV {
			t.Errorf("round %d: translator not restored: TranslationsVersion %q, want %q", round, got, wantTV)
		}
		if got := DefaultLocale(); got != "en" {
			t.Errorf("round %d: DefaultLocale %q, want en", round, got)
		}
		if got, _ := currencyCode.Load().(string); got != "GBP" {
			t.Errorf("round %d: currency %q, want GBP", round, got)
		}
		if got := currentUIScale(); got != 1.0 {
			t.Errorf("round %d: ui scale %v, want 1", round, got)
		}
		if got := oskModeVal(); got != "auto" {
			t.Errorf("round %d: osk %q, want auto", round, got)
		}
		if got := effectsLevelVal(); got != "full" {
			t.Errorf("round %d: effects %q, want full", round, got)
		}
		if got := orderTypePromptModeVal(); got != "top" {
			t.Errorf("round %d: order-type prompt %q, want top", round, got)
		}
		if got := idleLockSecs.Load(); got != 0 {
			t.Errorf("round %d: idle lock %d, want 0", round, got)
		}
		if got := LocaleGeneration(); got != 0 {
			t.Errorf("round %d: locale generation %d, want 0", round, got)
		}
		if on, _ := kioskMode.Load().(bool); on {
			t.Errorf("round %d: kiosk still on", round)
		}
		if on, _ := selfOrderMode.Load().(bool); on {
			t.Errorf("round %d: self-order still on", round)
		}
		if got, _ := displayMode.Load().(string); got != "" {
			t.Errorf("round %d: display mode %q, want empty", round, got)
		}
		if got := currentThemeVal(); got != "" {
			t.Errorf("round %d: theme %q, want empty", round, got)
		}
		if src, _ := railAmendmentsSource.Load().(func() []uislot.Amendment); src != nil {
			t.Errorf("round %d: rail amendments source not restored", round)
		}
		if check, _ := railVisibilitySource.Load().(func(*http.Request, string) bool); check != nil {
			t.Errorf("round %d: rail visibility source not restored", round)
		}
		// Funcs aren't comparable; tell them apart by what they return.
		if CrossDeviceLinkActionable() != cross() || UpdateInstallBridge() != bridge() || UpdateAvailable() != avail() {
			t.Errorf("round %d: platform seams not restored", round)
		}
	}
}
