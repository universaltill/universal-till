//go:build !windows

package selfupdate

import "context"

// queryAuthenticode only runs on Windows; Apply never routes here elsewhere.
func queryAuthenticode(_ context.Context, _ string) ([]byte, error) { return nil, errNotWindows }
