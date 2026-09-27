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
	p, on := logging.ResolveFile(logEnv, shellDataDir(dataEnv, defaultDataDir), goos, "desktop.log")
	if !on {
		return "", false
	}
	return filepath.Join(filepath.Dir(p), "desktop.log"), true
}

// shellDataDir is the till's data directory as the shell sees it: dataEnv
// (UT_DATA_DIR) when set, else defaultDataDir (paths.Default()). desktop.log
// and, on Windows, the WebView2 user data folder (ut-docs#2761,
// webview2_windows.go) live under it.
func shellDataDir(dataEnv, defaultDataDir string) string {
	if dataEnv != "" {
		return dataEnv
	}
	return defaultDataDir
}
