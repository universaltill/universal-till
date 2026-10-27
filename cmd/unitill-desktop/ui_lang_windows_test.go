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
