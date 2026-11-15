package locales_test

// A menu path in a right-to-left locale reads right to left, so its steps are
// joined with "←" (fa, ar). A "→" there points the reader backwards through
// the menu (ut-docs#5641). This test fails on any "→" in a string of an rtl
// locale. It checks every string, not only menu paths: an rtl string that
// ever needs a literal "→" should be reworded. The RTL decision is
// httpx.IsRTL, so the set of rtl locales has one home (an external test
// package keeps web/locales free of an httpx import).

import (
	"encoding/json"
	"io/fs"
	"sort"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/web/locales"
)

// ltrArrowKeys returns the sorted keys of an rtl locale's strings that
// contain "→". An ltr locale returns nothing.
func ltrArrowKeys(locale string, m map[string]string) []string {
	if !httpx.IsRTL(locale) {
		return nil
	}
	var keys []string
	for k, v := range m {
		if strings.Contains(v, "→") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

func TestLTRArrowKeysHelper(t *testing.T) {
	m := map[string]string{"a": "تنظیمات → ثبت صندوق", "b": "تنظیمات ← ثبت صندوق"}
	if got := ltrArrowKeys("fa", m); len(got) != 1 || got[0] != "a" {
		t.Fatalf("rtl: got %v, want [a]", got)
	}
	if got := ltrArrowKeys("en", map[string]string{"a": "Settings → Tills"}); len(got) != 0 {
		t.Fatalf("ltr: got %v, want none", got)
	}
}

func TestRTLLocalesUseRTLPathArrow(t *testing.T) {
	names, err := fs.Glob(locales.FS, "*.json")
	if err != nil || len(names) == 0 {
		t.Fatalf("glob locales: %v (%d files)", err, len(names))
	}
	rtlSeen := 0
	for _, name := range names {
		locale := strings.TrimSuffix(name, ".json")
		raw, err := fs.ReadFile(locales.FS, name)
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]string{}
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if httpx.IsRTL(locale) {
			rtlSeen++
		}
		for _, k := range ltrArrowKeys(locale, m) {
			t.Errorf("%s: %s uses \"→\" in a right-to-left locale; use \"←\"", name, k)
		}
	}
	if rtlSeen == 0 {
		t.Fatal("no rtl locale found; the check would pass vacuously")
	}
}
