// Exec seam: everything unitill-uninstall shells out to (systemctl,
// apt-get) goes through Runner, so tests fake it and never touch the real
// system — same dependency-injection shape cmd/unitill-desktop uses for
// its testable pieces.
package main

import (
	"io"
	"os/exec"
)

type Runner interface {
	Run(name string, args ...string) error
	LookPath(file string) (string, error)
}

// execRunner is the real implementation main() wires in: commands inherit
// the CLI's stdout/stderr so apt-get's own progress output stays visible
// to the operator.
type execRunner struct {
	stdout, stderr io.Writer
}

func (r *execRunner) Run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = r.stdout
	cmd.Stderr = r.stderr
	return cmd.Run()
}

func (r *execRunner) LookPath(file string) (string, error) {
	return exec.LookPath(file)
}
