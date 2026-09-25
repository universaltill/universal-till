package main

import (
	"path/filepath"

	"github.com/universaltill/universal-till/internal/logging"
)

// shellLogPath is where the desktop shell writes its own messages
// (ut-docs#2720): desktop.log beside the till's till.log. logEnv is
// UT_LOG_FILE (same switch as the till's; a relocated till.log puts
// desktop.log next to it), dataEnv is UT_DATA_DIR, defaultDataDir is
// paths.Default(). Kept free of build tags so plain `go test` covers it.
func shellLogPath(logEnv, dataEnv, defaultDataDir, goos string) (string, bool) {
	dataDir := dataEnv
	if dataDir == "" {
		dataDir = defaultDataDir
	}
	p, on := logging.ResolveFile(logEnv, dataDir, goos, "desktop.log")
	if !on {
		return "", false
	}
	return filepath.Join(filepath.Dir(p), "desktop.log"), true
}
