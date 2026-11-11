package config

import "testing"

func TestLookup_NoEnglishFallback(t *testing.T) {
	i := newLayerTestI18n(t)
	if v, ok := i.Lookup("de", "only.en"); ok {
		t.Errorf("Lookup(de, only.en) = %q,true; want no English fallback", v)
	}
	if v, ok := i.Lookup("de", "greet"); !ok || v != "Hallo (eingebaut)" {
		t.Errorf("Lookup(de, greet) = %q,%v", v, ok)
	}
	if v, ok := i.Lookup("de", "missing"); ok {
		t.Errorf("missing key echoed: %q", v)
	}
}

func TestLookup_BaseLang(t *testing.T) {
	i := newLayerTestI18n(t)
	if v, ok := i.Lookup("de-AT", "greet"); !ok || v != "Hallo (eingebaut)" {
		t.Errorf("Lookup(de-AT, greet) = %q,%v", v, ok)
	}
}

func TestLookup_ShopOverrideWins(t *testing.T) {
	i := newLayerTestI18n(t)
	i.SetShopOverrides(map[string]map[string]string{"de": {"greet": "Moin"}})
	if v, ok := i.Lookup("de", "greet"); !ok || v != "Moin" {
		t.Errorf("Lookup = %q,%v; want Moin", v, ok)
	}
}
