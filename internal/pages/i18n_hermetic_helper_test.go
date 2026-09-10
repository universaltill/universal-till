package pages

import (
	"slices"
	"testing"

	"github.com/universaltill/universal-till/internal/httpx"
)

// TestNewHermeticEnOnlyI18n_RestoresRealTranslatorOnCleanup pins ut-docs#2022:
// newHermeticEnOnlyI18n (setup_base_plugins_test.go) installs a hermetic,
// overlay-only, en-only translator via the process-global httpx.InitI18n but
// never restored the real web/locales bundle afterward — so it stayed wired
// for every test the Go test runner happened to schedule later in the same
// binary, invisible at the leaking test's own call site and dependent on
// file/test ordering. ut-docs#2015's own new test helper
// (newI18nForEntryOverlayTest) hit the identical hazard for real; this test
// exercises the same shape without relying on file-ordering luck: it calls
// newHermeticEnOnlyI18n inside a subtest (so its t.Cleanup fires when that
// subtest finishes, simulating "the leaking test has already run") and then
// asserts, from the outer test, that the real locale bundle (which ships
// ar/en/fa/tr, per TestMain's own doc comment) is back in place — not the
// hermetic fixture's en-only overlay.
func TestNewHermeticEnOnlyI18n_RestoresRealTranslatorOnCleanup(t *testing.T) {
	dp := newBasePluginTestDeps(t)

	t.Run("inner", func(t *testing.T) {
		newHermeticEnOnlyI18n(t, dp)
		if slices.Contains(httpx.AvailableLocales(), "ar") {
			t.Fatal("hermetic en-only translator unexpectedly already exposes the real bundle's locales — this test's own fixture is not proving anything")
		}
	})

	if !slices.Contains(httpx.AvailableLocales(), "ar") {
		t.Fatal("newHermeticEnOnlyI18n did not restore the real locale bundle once its calling test finished — the ut-docs#2022 leak is back: a later test in this binary would silently run against the hermetic en-only fixture instead of production locales")
	}
}
