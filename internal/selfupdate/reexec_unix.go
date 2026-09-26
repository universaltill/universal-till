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

// signalSelf asks this process to shut down gracefully (main.go's
// NotifyContext turns SIGTERM into the normal drain-and-close path). Used
// only under systemd, whose Restart=always then starts the swapped-in binary
// (ut-docs#2759).
func signalSelf() error {
	return syscall.Kill(os.Getpid(), syscall.SIGTERM)
}
