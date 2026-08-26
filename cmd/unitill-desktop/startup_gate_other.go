//go:build !(desktop && linux)

package main

// waitForSafeStartup is a no-op off the desktop&&linux path: ut-docs#1093 is
// a WebKitGTK-on-Wayland defect and does not affect the macOS or Windows
// shells, which use their own platform web views.
func waitForSafeStartup() {}
