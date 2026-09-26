//go:build !darwin

package selfupdate

import "context"

// applyMacApp only runs on macOS; on other platforms it's never reached
// (Apply guards on runtime.GOOS == "darwin"), but the symbol must exist.
func applyMacApp(_ context.Context, _, _ string, _ func() bool) error {
	return ErrUnsupported
}
