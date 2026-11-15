//go:build desktop && !darwin && !windows

package main

// platformPreferredLang: no OS-level hint beyond LC_ALL/LC_MESSAGES/LANG,
// which a Linux desktop session sets. macOS (webkit_darwin.go) and Windows
// (ui_lang_windows.go) ask the OS instead (ut-docs#5051).
func platformPreferredLang() string { return "" }
