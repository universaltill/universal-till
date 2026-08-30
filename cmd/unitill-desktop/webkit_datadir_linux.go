//go:build linux

// Pure data-directory resolution for the Linux WebKit cookie jar
// (ut-docs#1233) — deliberately free of the `desktop` build tag, like
// autostart.go/window_mode.go, so `go test ./...` (which never sets
// `-tags desktop`, see stub.go's own comment) actually exercises it. The
// cgo glue that turns this path into a real persistent WebKitCookieManager
// (webkit_linux.go) is the thin, hard-to-get-wrong wrapper around it.
package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// webkitDataDir resolves ~/.local/share/universal-till/webkit, honoring
// $XDG_DATA_HOME the same way os.UserConfigDir() honors $XDG_CONFIG_HOME
// (autostart_linux.go's own convention) — cookies are persistent user
// data, not config or cache, so $XDG_DATA_HOME is the correct base per the
// XDG Base Directory spec, even though the Go standard library has no
// os.UserDataDir() helper for it the way it does for config/cache.
//
// "universal-till", not "unitill": that's this product's one canonical
// Linux per-user data root (internal/paths.Default's own appNix, and the
// only directory packaging/linux/uninstall-unitill.sh offers to delete).
// "unitill" is reserved for *system* paths (/opt/unitill, /var/lib/unitill)
// and unit/desktop filenames elsewhere in this package — never the
// per-user XDG root. Getting this wrong would put a cookie file — the
// only place the raw session token lives on disk, since the DB stores
// only its hash — outside the one directory the uninstaller knows about,
// silently surviving an uninstall the user was told wiped shop data.
func webkitDataDir() (string, error) {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home dir: %w", err)
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "universal-till", "webkit"), nil
}
