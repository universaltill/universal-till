package config

import (
	"testing"
	"testing/fstest"
)

// newLayerTestI18n builds a translator with built-in en + de files, the shape
// of a till binary that ships German.
func newLayerTestI18n(t *testing.T) *I18n {
	t.Helper()
	i, err := NewI18nFS(fstest.MapFS{
		"en.json": {Data: []byte(`{"greet":"Hello","total":"Total %s","only.en":"English only","core.key":"Core"}`)},
		"de.json": {Data: []byte(`{"greet":"Hallo (eingebaut)","total":"Summe %s","core.key":"Kern"}`)},
	}, "en")
	if err != nil {
		t.Fatalf("NewI18nFS: %v", err)
	}
	return i
}

// ADR-0160 §6 lookup order, per locale: shop → fresh first-party → built-in
// → stale first-party → third-party (ut-docs#4369).
func TestPluginLayers_LookupOrder(t *testing.T) {
	i := newLayerTestI18n(t)
	i.SetPluginLocales(PluginLocales{
		FirstPartyFresh: map[string]map[string]string{"de": {"greet": "Hallo (Bundle neu)"}},
		FirstPartyStale: map[string]map[string]string{"de": {"core.key": "Kern (Bundle alt)", "stale.only": "Alt"}, "fr": {"greet": "Bonjour (ancien)"}},
		ThirdParty:      map[string]map[string]string{"de": {"core.key": "Gekapert", "stale.only": "Dritte", "third.only": "Dritte"}, "fr": {"greet": "Salut (tiers)", "x": "y"}},
	})

	cases := []struct{ loc, key, want, why string }{
		{"de", "greet", "Hallo (Bundle neu)", "a fresh first-party bundle beats the built-in file"},
		{"de", "core.key", "Kern", "a stale bundle never overrides a newer built-in string"},
		{"de", "stale.only", "Alt", "a stale first-party bundle beats a third-party plugin"},
		{"de", "third.only", "Dritte", "a third-party plugin fills gaps"},
		{"fr", "greet", "Bonjour (ancien)", "first-party beats third-party in a locale with no built-in file"},
		{"de", "only.en", "English only", "a key missing from every German layer shows English, never blank or the key"},
		{"fr", "core.key", "Core", "a key missing from a bundle-only locale shows English"},
	}
	for _, c := range cases {
		if got := i.T(c.loc, c.key); got != c.want {
			t.Errorf("T(%s,%s) = %q, want %q — %s", c.loc, c.key, got, c.want, c.why)
		}
	}

	i.SetShopOverrides(map[string]map[string]string{"de": {"greet": "Moin"}})
	if got := i.T("de", "greet"); got != "Moin" {
		t.Errorf("shop override must beat a fresh bundle, got %q", got)
	}
}

// A third-party plugin can't override a core string in a locale the till
// ships — the ADR-0010 rule ADR-0160 keeps for third parties.
func TestPluginLayers_ThirdPartyNeverOverridesCore(t *testing.T) {
	i := newLayerTestI18n(t)
	i.SetPluginLocales(PluginLocales{ThirdParty: map[string]map[string]string{
		"en": {"greet": "Pwned"},
		"de": {"greet": "Gekapert"},
	}})
	if got := i.T("en", "greet"); got != "Hello" {
		t.Errorf("T(en,greet) = %q", got)
	}
	if got := i.T("de", "greet"); got != "Hallo (eingebaut)" {
		t.Errorf("T(de,greet) = %q", got)
	}
}

// ADR-0160 §5: the till drops a plugin string whose placeholders differ from
// English at load; the key falls through to the next layer.
func TestPluginLayers_PlaceholderMismatchIsDropped(t *testing.T) {
	i := newLayerTestI18n(t)
	i.SetPluginLocales(PluginLocales{
		FirstPartyFresh: map[string]map[string]string{"de": {"total": "Summe %d"}}, // %s in English
		ThirdParty: map[string]map[string]string{
			"fr": {"total": "Total", "plugin.k": "%s et %s", "plugin.ok": "%[2]s / %[1]s", "pct": "50 % de remise"},
			"en": {"plugin.k": "%s", "plugin.ok": "%[1]s / %[2]s", "pct": "50% off"},
		},
	})
	if got := i.T("de", "total"); got != "Summe %s" {
		t.Errorf("mismatched fresh bundle string not dropped: T(de,total) = %q, want the built-in", got)
	}
	if got := i.T("fr", "total"); got != "Total %s" {
		t.Errorf("a string missing English's %%s must fall through to English, got %q", got)
	}
	if got := i.T("fr", "plugin.k"); got != "%s" {
		t.Errorf("plugin key checked against the plugin's own en.json: got %q", got)
	}
	if got := i.T("fr", "plugin.ok"); got != "%[2]s / %[1]s" {
		t.Errorf("reordered indexed verbs are the same set: got %q", got)
	}
	if got := i.T("fr", "pct"); got != "50 % de remise" {
		t.Errorf("a bare percent sign in prose is not a placeholder: got %q", got)
	}
}

func TestSamePlaceholders(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"Total %s", "Summe %s", true},
		{"%s of %d", "%d von %s", true},
		{"%s", "%d", false},
		{"%s", "", false},
		{"100%% sure %s", "%s sicher", true},
		{"19%, 7% or 0% of", "19 %, 7 % oder 0 % von", true},
		{"%-5.2f", "%-5.2f", true},
		{"%[1]s %[1]s", "%[1]s", false},
		{"no verbs", "keine", true},
	}
	for _, c := range cases {
		if got := samePlaceholders(c.a, c.b); got != c.want {
			t.Errorf("samePlaceholders(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// Entries resolves in T's order and lists bundle-only keys; Has and
// Available see both first-party tiers.
func TestPluginLayers_EntriesHasAvailable(t *testing.T) {
	i := newLayerTestI18n(t)
	i.SetPluginLocales(PluginLocales{
		FirstPartyFresh: map[string]map[string]string{"de": {"greet": "Neu"}},
		FirstPartyStale: map[string]map[string]string{"pt": {"greet": "Olá"}, "de": {"core.key": "Alt", "bundle.only": "B"}},
	})
	got := map[string]TranslationEntry{}
	for _, e := range i.Entries("de") {
		got[e.Key] = e
	}
	if e := got["greet"]; e.Value != "Neu" || e.Source != "plugin" {
		t.Errorf("Entries greet = %+v, want the fresh bundle", e)
	}
	if e := got["core.key"]; e.Value != "Kern" || e.Source != "base" {
		t.Errorf("Entries core.key = %+v, want the built-in over the stale bundle", e)
	}
	if e := got["bundle.only"]; e.Value != "B" || e.Source != "plugin" {
		t.Errorf("Entries bundle.only = %+v", e)
	}
	if !i.Has("bundle.only") {
		t.Error("Has misses a first-party-only key")
	}
	avail := map[string]bool{}
	for _, l := range i.Available() {
		avail[l] = true
	}
	if !avail["pt"] {
		t.Errorf("Available %v misses a first-party-only locale", i.Available())
	}
}

// SetPluginLocales keeps the caller's maps intact (the plugin manager owns
// them) and moves Generation once.
func TestSetPluginLocales_DoesNotMutateAndBumpsGeneration(t *testing.T) {
	i := newLayerTestI18n(t)
	in := map[string]map[string]string{"de": {"total": "Summe %d", "x": "y"}}
	g := i.Generation()
	i.SetPluginLocales(PluginLocales{FirstPartyFresh: in})
	if _, ok := in["de"]["total"]; !ok {
		t.Fatal("caller's map lost the dropped key")
	}
	if i.Generation() != g+1 {
		t.Fatalf("Generation moved %d times, want 1", i.Generation()-g)
	}
}
