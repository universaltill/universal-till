//go:build windows

package main

import (
	"strings"

	"golang.org/x/sys/windows"
)

// platformPreferredLang is the user's first Windows UI language ("tr-TR"),
// used for the startup-failure page because a GUI-launched app rarely has
// LANG (ut-docs#5051); "" when the call fails.
func platformPreferredLang() string {
	langs, err := windows.GetUserPreferredUILanguages(windows.MUI_LANGUAGE_NAME)
	if err != nil {
		return ""
	}
	return firstUILanguage(langs)
}

// firstUILanguage returns the first non-blank entry of an OS-ordered UI
// language list ("tr-TR"), trimmed; "" when there is none (ut-docs#5051).
// Windows-only: its one caller is above, so the Linux deadcode pass would
// flag it in an untagged file.
func firstUILanguage(langs []string) string {
	for _, l := range langs {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}
