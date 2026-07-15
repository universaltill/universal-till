//go:build !windows

package selfupdate

import (
	"os"
	"syscall"
)

// reexec replaces this process image with the freshly-installed binary (same
// PID). The listening socket has CLOEXEC set, so the port is released and the
// new process rebinds it cleanly — no child, no port race.
func reexec(exe string) error {
	return syscall.Exec(exe, os.Args, os.Environ())
}
