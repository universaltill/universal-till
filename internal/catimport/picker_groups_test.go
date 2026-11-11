package catimport

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func stubLookup(m map[string]string) LabelLookup {
	return func(loc, key string) (string, bool) {
		v, ok := m[loc+"|"+key]
		return v, ok
	}
}

func pickerKeywords(t *testing.T, groups []BuiltinIconGroup, key string) string {
	t.Helper()
	for _, g := range groups {
		for _, ic := range g.Icons {
			if ic.Key == key {
				return ic.Keywords
			}
		}
	}
	t.Fatalf("icon %q not found", key)
	return ""
}

func hasWord(kw, w string) bool {
	for _, f := range strings.Fields(kw) {
		if f == w {
			return true
		}
	}
	return false
}

func TestPickerIconGroups_SameShapeAsBuiltinIconGroups(t *testing.T) {
	a, b := BuiltinIconGroups(), PickerIconGroups([]string{"tr"}, nil)
	if len(a) != len(b) {
		t.Fatalf("groups %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].Key != b[i].Key || a[i].I18nKey != b[i].I18nKey || len(a[i].Icons) != len(b[i].Icons) {
			t.Fatalf("group %d differs", i)
		}
		for j := range a[i].Icons {
			x, y := a[i].Icons[j], b[i].Icons[j]
			x.Keywords, y.Keywords = "", ""
			if x != y {
				t.Fatalf("icon %d/%d differs: %+v vs %+v", i, j, x, y)
			}
		}
	}
}

func TestPickerIconGroups_EnglishOnlyKeepsEveryEnglishKeyword(t *testing.T) {
	orig := map[string]string{}
	for _, ic := range BuiltinIcons() {
		orig[ic.Key] = ic.Keywords
	}
	got := PickerIconGroups([]string{"en"}, stubLookup(nil))
	for k, kws := range orig {
		have := pickerKeywords(t, got, k)
		for _, w := range strings.Fields(kws) {
			if !strings.Contains(have, FoldForSearch(w)) {
				t.Errorf("%s: keyword %q lost (have %q)", k, w, have)
			}
		}
	}
}

func TestPickerIconGroups_AddsShopLanguageLabels(t *testing.T) {
	lk := stubLookup(map[string]string{
		"en|catalog.builtin_icon.coffee": "Coffee",
		"tr|catalog.builtin_icon.coffee": "Kahve",
		"ar|catalog.builtin_icon.coffee": "قهوة",
		"tr|catalog.builtin_icon.tea":    "Çay",
	})
	g := PickerIconGroups([]string{"tr", "ar"}, lk)
	c := pickerKeywords(t, g, "coffee")
	for _, w := range []string{"kahve", "قهوه", "coffee"} {
		if !hasWord(c, w) {
			t.Errorf("coffee missing %q in %q", w, c)
		}
	}
	if !hasWord(pickerKeywords(t, g, "tea"), "cay") {
		t.Error("tea missing cay")
	}
}

func TestPickerIconGroups_MissingLabelAddsNothing(t *testing.T) {
	lk := stubLookup(map[string]string{"en|catalog.builtin_icon.coffee": "Coffee"})
	g := PickerIconGroups([]string{"de"}, lk)
	for _, grp := range g {
		for _, ic := range grp.Icons {
			if strings.Contains(ic.Keywords, "catalog.builtin_icon") {
				t.Fatalf("%s leaks key: %q", ic.Key, ic.Keywords)
			}
		}
	}
	if n := strings.Count(pickerKeywords(t, g, "coffee"), "coffee "); n > 1 {
		t.Errorf("coffee repeated")
	}
}

func TestPickerIconGroups_DedupesFoldedWords(t *testing.T) {
	lk := stubLookup(map[string]string{"en|catalog.builtin_icon.coffee": "Coffee", "tr|catalog.builtin_icon.coffee": "COFFEE"})
	kw := pickerKeywords(t, PickerIconGroups([]string{"tr"}, lk), "coffee")
	seen := map[string]bool{}
	for _, w := range strings.Fields(kw) {
		if seen[w] {
			t.Fatalf("duplicate %q in %q", w, kw)
		}
		seen[w] = true
	}
	if !seen["coffee"] {
		t.Fatal("coffee missing")
	}
}

func TestPickerIconGroups_NilLookup(t *testing.T) {
	kw := pickerKeywords(t, PickerIconGroups([]string{"tr"}, nil), "coffee")
	if !hasWord(kw, "coffee") || strings.Contains(kw, "catalog.") {
		t.Fatalf("kw = %q", kw)
	}
}

func TestFoldForSearch_ParityFixture(t *testing.T) {
	b, err := os.ReadFile("testdata/search_fold_parity.json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct{ In, Out string }
	if err := json.Unmarshal(b, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) < 20 {
		t.Fatalf("fixture too small: %d", len(rows))
	}
	for _, r := range rows {
		if got := FoldForSearch(r.In); got != r.Out {
			t.Errorf("FoldForSearch(%q) = %q, want %q", r.In, got, r.Out)
		}
		if got := FoldForSearch(r.Out); got != r.Out {
			t.Errorf("not idempotent on %q: %q", r.Out, got)
		}
	}
}
