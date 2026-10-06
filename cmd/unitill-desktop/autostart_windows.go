//go:build desktop && windows

package main

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// reconcileAutostart writes or removes the current user's Startup-folder
// shortcut to THIS executable — unitill-desktop, the process with the
// window, never the unitill-pos server it spawns (ut-docs#610; the Linux
// review's M2 finding applies here too). Called on every launch with the
// persisted launch_on_startup preference, like autostart_linux.go.
func reconcileAutostart(enabled bool) error {
	dir, err := windows.KnownFolderPath(windows.FOLDERID_Startup, 0)
	if err != nil {
		return fmt.Errorf("resolve Startup folder: %w", err)
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}
	return reconcileStartupShortcut(dir, enabled, exe)
}
