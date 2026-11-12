package catimport

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestIconMatcher_RealBuiltinLocales proves the feature works on merge: the
// shipped tr/fa/ar locale files already carry translated tile labels.
func TestIconMatcher_RealBuiltinLocales(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..")
	load := func(loc string) map[string]string {
		b, err := os.ReadFile(filepath.Join(root, "web", "locales", loc+".json"))
		if err != nil {
			t.Fatalf("read %s: %v", loc, err)
		}
		m := map[string]string{}
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("parse %s: %v", loc, err)
		}
		return m
	}
	bundles := map[string]map[string]string{"tr": load("tr"), "fa": load("fa"), "ar": load("ar")}
	lookup := func(loc, key string) (string, bool) { v, ok := bundles[loc][key]; return v, ok }
	cases := []struct{ lang, name, want string }{
		{"tr", "Türk Kahvesi", "coffee"}, {"tr", "TÜRK KAHVESİ", "coffee"},
		{"fa", "قهوه", "coffee"}, {"ar", "القهوة", "coffee"},
	}
	for _, c := range cases {
		if got := NewIconMatcher([]string{c.lang, "en"}, lookup).Icon(c.name, ""); got != c.want {
			t.Errorf("[%s] %q = %q, want %q", c.lang, c.name, got, c.want)
		}
	}
}
