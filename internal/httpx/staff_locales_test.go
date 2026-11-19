package httpx

import (
	"reflect"
	"testing"
)

func TestStaffLocalesFor(t *testing.T) {
	avail := []string{"ar", "de", "en", "fa", "tr"}
	cases := []struct {
		name, stored, def string
		want              []string
	}{
		{"unset: default plus English", "", "de", []string{"de", "en"}},
		{"unset, regional default tag", "", "de-DE", []string{"de", "en"}},
		{"unset, English shop", "", "en", []string{"en"}},
		{"selection kept in installed order", "tr,de", "de", []string{"de", "tr"}},
		{"default always included", "tr", "de", []string{"de", "tr"}},
		{"uninstalled entries dropped", "de, xx ,fa", "de", []string{"de", "fa"}},
		{"only uninstalled entries: default alone", "xx", "de", []string{"de"}},
		{"default not installed: selection only", "tr", "pt-PT", []string{"tr"}},
		{"default not installed, unset: English", "", "pt-PT", []string{"en"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := StaffLocalesFor(c.stored, c.def, avail); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("StaffLocalesFor(%q, %q) = %v, want %v", c.stored, c.def, got, c.want)
			}
		})
	}
}

func TestParseStaffLocales(t *testing.T) {
	if got := ParseStaffLocales(" de ,, en,"); !reflect.DeepEqual(got, []string{"de", "en"}) {
		t.Fatalf("got %v", got)
	}
	if got := ParseStaffLocales(""); got != nil {
		t.Fatalf("got %v, want nil", got)
	}
}

func TestShopLanguagesFor(t *testing.T) {
	avail := []string{"ar", "de", "en", "fa", "tr"}
	cases := []struct {
		name, stored, def string
		want              []string
	}{
		{"unset: default first then en", "", "de", []string{"de", "en"}},
		{"default first, rest in installed order", "tr,ar", "de", []string{"de", "ar", "tr"}},
		{"deduped when default also selected", "de,tr", "de", []string{"de", "tr"}},
		{"English shop", "", "en", []string{"en"}},
		{"default not installed", "tr", "pt-PT", []string{"tr"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ShopLanguagesFor(c.stored, c.def, avail); !reflect.DeepEqual(got, c.want) {
				t.Errorf("ShopLanguagesFor(%q,%q) = %v, want %v", c.stored, c.def, got, c.want)
			}
		})
	}
}

func TestTExact_NoTranslator(t *testing.T) {
	InitI18n(nil, "en")
	if v, ok := TExact("de", "x"); ok || v != "" {
		t.Errorf("TExact = %q,%v", v, ok)
	}
}
