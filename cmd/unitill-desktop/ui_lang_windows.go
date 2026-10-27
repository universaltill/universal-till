//go:build windows

package main

import "golang.org/x/sys/windows"

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
