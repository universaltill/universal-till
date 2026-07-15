// Package paths resolves where the till keeps its mutable data (database,
// backups, installed plugins, caches, item images, the enrolment identity).
//
// It must be a STABLE per-user/per-machine location so data survives across
// version upgrades — extracting a new archive into a new folder must not start
// from an empty database. Everything hangs off one root:
//
//   - macOS   ~/Library/Application Support/UniversalTill
//   - Windows %LOCALAPPDATA%\UniversalTill
//   - Linux   $XDG_DATA_HOME/universal-till (or ~/.local/share/universal-till)
//   - server  set UT_DATA_DIR explicitly (the .deb uses /var/lib/unitill)
//
// The root is overridable with UT_DATA_DIR; individual files can still be
// overridden (e.g. UT_DB_PATH). Init is called once during config load.
package paths

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
)

var dataDir atomic.Value // string

// Init records the resolved data directory. Call once, early, before anything
// reads a data path.
func Init(dir string) { dataDir.Store(dir) }

// DataDir returns the resolved data root (falls back to ./data if Init hasn't
// run yet, preserving the pre-refactor layout for any early caller/test).
func DataDir() string {
	if v, ok := dataDir.Load().(string); ok && v != "" {
		return v
	}
	return "./data"
}

// Data joins parts onto the data root.
func Data(parts ...string) string {
	return filepath.Join(append([]string{DataDir()}, parts...)...)
}

// Plugins is the installed-plugins tree (formerly ./data/plugins).
func Plugins(parts ...string) string {
	return filepath.Join(append([]string{DataDir(), "plugins"}, parts...)...)
}

// MigrateLegacyDB moves an old cwd-relative database into the resolved data
// directory the first time a build with the stable data dir runs, so an
// in-place upgrade (extracting a new version over the old folder) keeps its
// data. Best-effort: it only acts when the target is absent and a legacy
// ./data/unitill-pos.db exists, and it copies (leaving the original intact).
func MigrateLegacyDB(dbPath string) {
	const legacy = "data/unitill-pos.db" // the old default, relative to cwd
	if dbPath == legacy || dbPath == "./"+legacy {
		return // still using the legacy location
	}
	if _, err := os.Stat(dbPath); err == nil {
		return // target already has a database
	}
	if _, err := os.Stat(legacy); err != nil {
		return // no legacy database to migrate
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return
	}
	src, err := os.Open(legacy)
	if err != nil {
		return
	}
	defer src.Close()
	dst, err := os.OpenFile(dbPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer dst.Close()
	if _, err := io.Copy(dst, src); err != nil {
		_ = os.Remove(dbPath) // don't leave a half-copied DB
	}
}

// Default returns the conventional per-user data directory for this OS. Used as
// the UT_DATA_DIR default. Falls back to ./data when a home dir can't be found
// (keeps the app working in odd environments).
func Default() string {
	const appWin, appNix = "UniversalTill", "universal-till"
	switch runtime.GOOS {
	case "windows":
		if d := os.Getenv("LOCALAPPDATA"); d != "" {
			return filepath.Join(d, appWin)
		}
	case "darwin":
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, "Library", "Application Support", appWin)
		}
	default: // linux, bsd, …
		if d := os.Getenv("XDG_DATA_HOME"); d != "" {
			return filepath.Join(d, appNix)
		}
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, ".local", "share", appNix)
		}
	}
	return "./data"
}
