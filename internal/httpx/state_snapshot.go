package httpx

import (
	"net/http"

	"github.com/universaltill/universal-till/internal/uislot"
)

// SnapshotStateForTests captures every process-wide value this package publishes
// (translator, default locale, currency, UI scale, OSK/effects/order-type
// modes, idle lock, locale generation, kiosk/self-order/display mode, theme,
// rail sources and the platform seams) and returns a func that puts them all
// back. The func can be called any number of times. It restores which
// translator is wired, not that translator's own overlay state: a test that
// installs an overlay into a shared *config.I18n undoes that itself.
//
// Test support (ut-docs#3822): handlers under test publish settings here
// (the setup wizard, settings saves, cloud set_setting), so in a shuffled
// run the next test inherited another test's lang="tr" or ui-scale. A test
// package takes one baseline in TestMain and restores it in t.Cleanup from
// its shared helpers. Production code never calls this, so the deadcode
// guard lists it in scripts/ci/deadcode-baseline.txt (test-only reachable,
// like ResetCacheForTests).
func SnapshotStateForTests() (restore func()) {
	tr := i18nRef.Load()
	locale := defaultLocale.Load()
	currency := currencyCode.Load()
	scale := uiScale.Load()
	osk := oskMode.Load()
	fx := effectsLevel.Load()
	orderPrompt := orderTypePromptMode.Load()
	idle := idleLockSecs.Load()
	localeGen := localeGeneration.Load()
	kiosk := kioskMode.Load()
	selfOrder := selfOrderMode.Load()
	display := displayMode.Load()
	theme := currentTheme.Load()
	amendments := railAmendmentsSource.Load()
	visibility := railVisibilitySource.Load()
	cross, bridge, avail := CrossDeviceLinkActionable, UpdateInstallBridge, UpdateAvailable

	// An atomic.Value can't go back to "never stored", so a value unset at
	// snapshot time is restored as its type's zero value — which every
	// getter here already reads as its default.
	restoreValue := func(prev, zero any) any {
		if prev == nil {
			return zero
		}
		return prev
	}
	return func() {
		i18nRef.Store(restoreValue(tr, (*wiredTranslator)(nil)))
		defaultLocale.Store(restoreValue(locale, ""))
		currencyCode.Store(restoreValue(currency, ""))
		uiScale.Store(restoreValue(scale, 1.0))
		oskMode.Store(restoreValue(osk, ""))
		effectsLevel.Store(restoreValue(fx, ""))
		orderTypePromptMode.Store(restoreValue(orderPrompt, ""))
		idleLockSecs.Store(idle)
		localeGeneration.Store(localeGen)
		kioskMode.Store(restoreValue(kiosk, false))
		selfOrderMode.Store(restoreValue(selfOrder, false))
		displayMode.Store(restoreValue(display, ""))
		currentTheme.Store(restoreValue(theme, ""))
		railAmendmentsSource.Store(restoreValue(amendments, (func() []uislot.Amendment)(nil)))
		railVisibilitySource.Store(restoreValue(visibility, (func(*http.Request, string) bool)(nil)))
		CrossDeviceLinkActionable, UpdateInstallBridge, UpdateAvailable = cross, bridge, avail
	}
}
