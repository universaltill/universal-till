package config

import "testing"

func newTestI18n(t *testing.T) *I18n {
	t.Helper()
	dir := t.TempDir()
	i := &I18n{
		messages: map[string]map[string]string{
			"en": {"basket.total": "Total", "only.base": "Base"},
			"fa": {"basket.total": "جمع"},
		},
		overlays: map[string]map[string]string{
			"fa": {"plugin.faq.menu": "راهنما"},
		},
		shop:     map[string]map[string]string{},
		fallback: "en",
	}
	_ = dir
	return i
}

func TestShopOverridesWinOverEverything(t *testing.T) {
	i := newTestI18n(t)

	if got := i.T("fa", "basket.total"); got != "جمع" {
		t.Fatalf("base fa = %q", got)
	}
	i.SetShopOverrides(map[string]map[string]string{
		"fa": {"basket.total": "جمع کل", "plugin.faq.menu": "پرسش‌ها"},
	})
	if got := i.T("fa", "basket.total"); got != "جمع کل" {
		t.Fatalf("shop override must win over base, got %q", got)
	}
	if got := i.T("fa", "plugin.faq.menu"); got != "پرسش‌ها" {
		t.Fatalf("shop override must win over plugin overlay, got %q", got)
	}
	// clearing restores the layers underneath
	i.SetShopOverrides(nil)
	if got := i.T("fa", "plugin.faq.menu"); got != "راهنما" {
		t.Fatalf("after clear, plugin overlay must apply, got %q", got)
	}
}

func TestEntriesUnionAndSources(t *testing.T) {
	i := newTestI18n(t)
	i.SetShopOverrides(map[string]map[string]string{"fa": {"only.shop": "فقط فروشگاه"}})

	entries := i.Entries("fa")
	bySrc := map[string]TranslationEntry{}
	for _, e := range entries {
		bySrc[e.Key] = e
	}
	if e := bySrc["basket.total"]; e.Source != "base" || e.Value != "جمع" || e.Reference != "Total" {
		t.Fatalf("basket.total = %+v", e)
	}
	if e := bySrc["plugin.faq.menu"]; e.Source != "plugin" {
		t.Fatalf("plugin.faq.menu source = %q", e.Source)
	}
	if e := bySrc["only.shop"]; e.Source != "shop" {
		t.Fatalf("only.shop source = %q", e.Source)
	}
	// key that exists only in the fallback base: untranslated for fa
	if e := bySrc["only.base"]; e.Source != "" || e.Reference != "Base" {
		t.Fatalf("only.base = %+v", e)
	}
}

// Mirrors TestT_MissingKeyInNonEnglishFallbackFallsBackToEnglish but for
// Entries(): a shop's store.locale (ut-docs#861) can be a non-English,
// entirely overlay-provided locale (a language-pack plugin) that's missing a
// key en.json has (lang-pack-drift CI tolerates this drift). T() already
// falls back to English for that key (ut-docs#995) — but before this fix,
// Entries()'s key-collection loop only scanned {fallback, baseLang(fallback),
// locale, baseLang(locale)}, never baseLocale unconditionally, so the
// translation editor never listed the key as needing translation at all
// (ut-docs#997).
func TestEntries_MissingKeyInNonEnglishFallbackStillListed(t *testing.T) {
	i := &I18n{
		messages: map[string]map[string]string{
			"en": {"basket.total": "Total", "only.en": "English only"},
		},
		overlays: map[string]map[string]string{
			"de": {"basket.total": "Summe"},
		},
		shop:     map[string]map[string]string{},
		fallback: "de", // shop's configured store.locale
	}

	entries := i.Entries("de")
	bySrc := map[string]TranslationEntry{}
	for _, e := range entries {
		bySrc[e.Key] = e
	}
	e, ok := bySrc["only.en"]
	if !ok {
		t.Fatalf("Entries(de) missing %q entirely, want it listed as untranslated", "only.en")
	}
	if e.Source != "" || e.Value != "" {
		t.Fatalf("only.en = %+v, want untranslated (no de/overlay value)", e)
	}
	if e.Reference != "English only" {
		t.Fatalf("only.en.Reference = %q, want the English fallback text", e.Reference)
	}
}

// SetOverlays (language-pack plugin translations) had zero direct test
// coverage before this batch — only ever exercised transitively through T()
// via a hand-built struct literal, never through the setter itself.
func TestSetOverlaysReplacesAtomically(t *testing.T) {
	i := newTestI18n(t)

	if got := i.T("fa", "plugin.faq.menu"); got != "راهنما" {
		t.Fatalf("base overlay = %q", got)
	}
	i.SetOverlays(map[string]map[string]string{
		"fa": {"plugin.faq.menu": "پرسش‌ها"},
	})
	if got := i.T("fa", "plugin.faq.menu"); got != "پرسش‌ها" {
		t.Fatalf("after SetOverlays = %q, want the new overlay value", got)
	}
	// A resync with nil (e.g. every plugin uninstalled) must clear existing
	// overlays rather than panic on a nil map.
	i.SetOverlays(nil)
	if got := i.T("fa", "plugin.faq.menu"); got != "plugin.faq.menu" {
		t.Fatalf("after SetOverlays(nil) = %q, want the untranslated key back", got)
	}
}

// A shop can set its default locale (store.locale, ut-docs#861) to a
// non-English locale, e.g. one supplied entirely by an overlay-provided
// language-pack plugin. Those packs are allowed to lag behind en.json
// (lang-pack-drift CI is advisory, not blocking, per its own design) — so a
// key missing from that fallback locale must still resolve to the English
// text, not the raw key (ut-docs#995).
func TestT_MissingKeyInNonEnglishFallbackFallsBackToEnglish(t *testing.T) {
	i := &I18n{
		messages: map[string]map[string]string{
			"en": {"basket.total": "Total", "only.en": "English only"},
		},
		overlays: map[string]map[string]string{
			// "de" is entirely overlay-provided (a language-pack plugin) and
			// deliberately missing "only.en" — the drift lang-pack-drift CI
			// tolerates.
			"de": {"basket.total": "Summe"},
		},
		shop:     map[string]map[string]string{},
		fallback: "de", // shop's configured store.locale
	}

	if got := i.T("de", "basket.total"); got != "Summe" {
		t.Fatalf("T(de, basket.total) = %q, want the de translation", got)
	}
	if got := i.T("de", "only.en"); got != "English only" {
		t.Fatalf("T(de, only.en) = %q, want the English fallback, not the raw key", got)
	}
}

// Available() had zero direct test coverage before this batch.
func TestAvailableListsLocalesWithRealTranslations(t *testing.T) {
	i := newTestI18n(t)
	// "en"/"fa" both carry base messages; add an overlay-only locale and an
	// empty one that must NOT count as "available" (len(m) > 0 gate).
	i.SetOverlays(map[string]map[string]string{
		"tr": {"basket.total": "Toplam"},
		"de": {}, // present but empty: must not appear
	})

	got := i.Available()
	want := []string{"en", "fa", "tr"}
	if len(got) != len(want) {
		t.Fatalf("Available() = %v, want %v", got, want)
	}
	for idx, w := range want {
		if got[idx] != w {
			t.Fatalf("Available() = %v, want %v (sorted)", got, want)
		}
	}
}
