//go:build desktop

package main

import "runtime"

// posBinaryName is the platform-specific name of the POS server executable that
// ships alongside the desktop shell.
func posBinaryName() string {
	if runtime.GOOS == "windows" {
		return "unitill-pos.exe"
	}
	return "unitill-pos"
}
