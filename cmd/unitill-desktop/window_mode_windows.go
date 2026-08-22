//go:build desktop && windows

package main

import webview "github.com/webview/webview_go"

// applyWindowMode is a no-op on Windows for now — real WebView2/win32
// fullscreen wiring is ut-docs#610's own scope (this card, #611, covers
// Linux only). Kept as an explicit stub rather than a shared default so
// #610 has one obvious place to land its implementation.
func applyWindowMode(w webview.WebView, mode string) {}
