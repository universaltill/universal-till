//go:build desktop && windows

package main

import (
	"os/exec"
	"syscall"
)

// configureChild keeps the spawned console-subsystem server invisible: the
// shell itself is built with -H windowsgui (no console), and without this the
// child unitill-pos.exe would still pop a terminal window.
func configureChild(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
}
