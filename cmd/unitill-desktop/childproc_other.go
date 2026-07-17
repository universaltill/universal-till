//go:build desktop && !windows

package main

import "os/exec"

// configureChild is a no-op outside Windows (there is no console window to
// suppress; the server child just inherits stdout/stderr).
func configureChild(_ *exec.Cmd) {}
