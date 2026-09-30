//go:build !windows && !ios && !android

package procrestart

import (
	"os"
	"syscall"
)

// supported: every desktop/server non-Windows target can exec in place.
// iOS and Android are excluded (reexec_mobile.go, ut-docs#3220).
const supported = true

// reexec replaces this process image with exe (same PID, same argv and
// environment). The listening socket and the data-directory lock both have
// CLOEXEC set, so they are released at the moment of exec and the new image
// rebinds/re-locks them cleanly — no child process, no port race. Identical
// to internal/selfupdate's reexec, which has proven this in production.
func reexec(exe string) error {
	return syscall.Exec(exe, os.Args, os.Environ())
}
