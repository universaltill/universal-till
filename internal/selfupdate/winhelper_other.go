//go:build !windows

package selfupdate

// startWindowsHelper only runs on Windows; Apply never routes here elsewhere.
func startWindowsHelper(_, _, _, _, _ string) error { return errNotWindows }
