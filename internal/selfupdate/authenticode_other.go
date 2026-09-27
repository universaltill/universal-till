//go:build !windows

package selfupdate

import "context"

// verifyAuthenticode only runs on Windows; Apply never routes here elsewhere.
func verifyAuthenticode(_ context.Context, _, _ string) error { return errNotWindows }
