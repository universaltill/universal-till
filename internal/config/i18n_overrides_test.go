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
