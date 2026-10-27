//go:build windows

package main

import "testing"

// TestWin32PreferredUILanguage is ut-docs#5051: the real Win32 call must
// yield a usable language (the name carries "Win32" so the windows-shell CI
// job's -run filter picks it up).
func TestWin32PreferredUILanguage(t *testing.T) {
	l := platformPreferredLang()
	if primarySubtag(l) == "" {
		t.Fatalf("platformPreferredLang() = %q, want a language with a primary subtag", l)
	}
}

// TestWin32FirstUILanguage: the first non-blank, trimmed entry of the OS list
// (ut-docs#5051).
func TestWin32FirstUILanguage(t *testing.T) {
	cases := []struct {
		name  string
		langs []string
		want  string
	}{
		{"nil", nil, ""},
		{"empty", []string{}, ""},
		{"blanks", []string{"", "  "}, ""},
		{"first", []string{"tr-TR", "en-US"}, "tr-TR"},
		{"skips blank, trims", []string{"", "  ar-EG ", "en-US"}, "ar-EG"},
	}
	for _, c := range cases {
		if got := firstUILanguage(c.langs); got != c.want {
			t.Errorf("%s: firstUILanguage = %q, want %q", c.name, got, c.want)
		}
	}
}
