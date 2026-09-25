package httpx

import (
	"testing"

	"github.com/universaltill/universal-till/internal/config"
)

// ut-docs#2501 review finding 4: TranslationsVersion must not depend on the
// translator's pointer identity (a freed translator's address can be reused
// by a new one with the same generation, and re-wiring an earlier
// translator would re-validate entries rendered under whatever it replaced).
// Every InitI18n must produce a version never seen before.
func TestTranslationsVersion_MonotonicAcrossInitI18n(t *testing.T) {
	t.Cleanup(func() { InitI18n(nil, "en") })
	a, err := config.NewI18nFS(fakeLocaleFS(`{"k":"A"}`), "en")
	if err != nil {
		t.Fatal(err)
	}
	b, err := config.NewI18nFS(fakeLocaleFS(`{"k":"B"}`), "en")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	record := func(step string) {
		t.Helper()
		v := TranslationsVersion()
		if seen[v] {
			t.Fatalf("%s: TranslationsVersion %q repeats an earlier version", step, v)
		}
		seen[v] = true
	}
	InitI18n(a, "en")
	record("wire a")
	a.SetOverlays(map[string]map[string]string{"en": {"k": "A2"}})
	record("a overlays replaced")
	InitI18n(b, "en")
	record("wire b")
	InitI18n(a, "en")
	record("re-wire a")
	if v1, v2 := TranslationsVersion(), TranslationsVersion(); v1 != v2 {
		t.Fatalf("TranslationsVersion not stable without a change: %q then %q", v1, v2)
	}
}
