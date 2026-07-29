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

// MigrateLegacyData brings an old cwd-relative ./data tree into the resolved
// data directory: the database (first run only) and any installed plugin
// bundles that are missing from the stable location. Plugin migration is
// per-plugin, not all-or-nothing, because earlier builds migrated ONLY the DB
// — the registry then pointed at bundles whose files (locales, wasm modules,
// assets) were left behind in ./data/plugins, silently breaking every
// installed plugin. Best-effort and copy-only: the legacy tree stays intact.
func MigrateLegacyData(dbPath string) {
	migrateLegacyDB(dbPath)
	migrateLegacyPlugins()
	migrateLegacyUploadedAssets()
}

// migrateLegacyUploadedAssets copies a shop's already-uploaded item/variant
// photos and receipt logo out of the release tree (web/public/assets) into
// the stable data directory, the FIRST time this runs after the fix that
// made new uploads go to the stable location instead. Without this, a shop
// that uploaded images under the old (broken) behavior would still lose
// them on the very next update, having to re-upload everything right after
// finally getting the fix — confirmed live 2026-07-29, this exact scenario.
//
// Deliberately narrow, not a blanket copy of web/public/assets: that tree
// also holds BUILT-IN default assets (the stock logo/icons) which must
// stay resolvable from the embedded/release tiers so a future release can
// still update them — copying them into the stable tier would freeze them
// at whatever version happened to exist during this one migration.
func migrateLegacyUploadedAssets() {
	legacyItems := filepath.Join("web", "public", "assets", "items")
	targetItems := Data("public", "assets", "items")
	if entries, err := os.ReadDir(legacyItems); err == nil {
		for _, e := range entries {
			dst := filepath.Join(targetItems, e.Name())
			if _, err := os.Stat(dst); err == nil {
				continue // already present in the stable location
			}
			src := filepath.Join(legacyItems, e.Name())
			if e.IsDir() {
				if err := os.MkdirAll(dst, 0o755); err != nil {
					continue
				}
				if err := os.CopyFS(dst, os.DirFS(src)); err != nil {
					_ = os.RemoveAll(dst) // don't leave a half-copied item's images
				}
			} else {
				_ = os.MkdirAll(targetItems, 0o755)
				copyFileBestEffort(src, dst)
			}
		}
	}

	legacyLogo := filepath.Join("web", "public", "assets", "logo", "receipt-logo.png")
	targetLogo := Data("public", "assets", "logo", "receipt-logo.png")
	if _, err := os.Stat(targetLogo); err != nil {
		if _, err := os.Stat(legacyLogo); err == nil {
			_ = os.MkdirAll(filepath.Dir(targetLogo), 0o755)
			copyFileBestEffort(legacyLogo, targetLogo)
		}
	}
}

func copyFileBestEffort(src, dst string) {
	in, err := os.Open(src)
	if err != nil {
		return
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		_ = os.Remove(dst) // don't leave a truncated file behind
	}
}

// migrateLegacyDB copies an old cwd-relative database into the resolved data
// directory. It only acts when the target is absent and a legacy
// ./data/unitill-pos.db exists.
func migrateLegacyDB(dbPath string) {
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

// transientPluginDirs are runtime scratch areas under plugins/ that must not
// be treated as installed plugin bundles.
var transientPluginDirs = map[string]bool{"cache": true, "tmp": true, "downloads": true, "auth": true, "versions": true}

// migrateLegacyPlugins copies each installed plugin bundle from the legacy
// ./data/plugins tree into the stable plugins dir when it is missing there.
func migrateLegacyPlugins() {
	const legacy = "data/plugins"
	target := Plugins()
	if abs, err := filepath.Abs(legacy); err == nil {
		if tabs, err := filepath.Abs(target); err == nil && tabs == abs {
			return // still using the legacy location
		}
	}
	entries, err := os.ReadDir(legacy)
	if err != nil {
		return // no legacy plugins to migrate
	}
	for _, e := range entries {
		if !e.IsDir() || transientPluginDirs[e.Name()] {
			continue
		}
		dst := filepath.Join(target, e.Name())
		if _, err := os.Stat(dst); err == nil {
			continue // already present in the stable location
		}
		src := filepath.Join(legacy, e.Name())
		if err := os.MkdirAll(dst, 0o755); err != nil {
			continue
		}
		if err := os.CopyFS(dst, os.DirFS(src)); err != nil {
			_ = os.RemoveAll(dst) // don't leave a half-copied bundle
		}
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
