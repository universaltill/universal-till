package plugins

import (
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/universaltill/universal-till/internal/logging"
)

// Plugin ids and versions are joined raw into filesystem paths
// (paths.Plugins()/<id>/<version>/) by the marketplace installer and the
// manual importer (ut-docs#2891), so both are restricted to a single safe
// path segment. Every real id is a lower-case reverse-DNS name
// (com.universaltill.tax-de); versions are semver.
var (
	pluginIDPattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	pluginVersionPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+_-]*$`)
)

const (
	maxPluginIDLen      = 128
	maxPluginVersionLen = 64
)

// windowsUnsafe reports a segment Windows would alias or refuse as a
// directory: a trailing dot is stripped (so "com.foo." is "com.foo"), and
// the reserved device names (with or without an extension) aren't files.
func windowsUnsafe(seg string) bool {
	if strings.HasSuffix(seg, ".") {
		return true
	}
	base := strings.ToLower(seg)
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	switch base {
	case "con", "prn", "aux", "nul":
		return true
	}
	if len(base) == 4 && (strings.HasPrefix(base, "com") || strings.HasPrefix(base, "lpt")) && base[3] >= '0' && base[3] <= '9' {
		return true
	}
	return false
}

// validatePluginID reports whether id is safe to use as a directory name.
func validatePluginID(id string) error {
	if len(id) > maxPluginIDLen || !pluginIDPattern.MatchString(id) || strings.Contains(id, "..") || windowsUnsafe(id) {
		return fmt.Errorf("invalid plugin id %q (lower-case letters, digits, '.', '_' and '-' only, starting with a letter or digit, at most %d characters)", id, maxPluginIDLen)
	}
	return nil
}

// validatePluginVersion reports whether v is safe to use as a directory name.
func validatePluginVersion(v string) error {
	if len(v) > maxPluginVersionLen || !pluginVersionPattern.MatchString(v) || strings.Contains(v, "..") || windowsUnsafe(v) {
		return fmt.Errorf("invalid plugin version %q", v)
	}
	return nil
}

// defaultRuntimeWarned de-duplicates the "no runtime declared" warning to
// one line per plugin id per process — manifests are re-parsed on every
// reload/rollback and the warning would otherwise repeat.
var defaultRuntimeWarned sync.Map

// defaultManifestRuntime is the runtime a manifest that omits "runtime"
// gets (ut-docs#2891): the sandbox, never the out-of-process "go" runtime.
const defaultManifestRuntime = "wasm"

func warnDefaultRuntimeOnce(id string) {
	if _, loaded := defaultRuntimeWarned.LoadOrStore(id, true); loaded {
		return
	}
	logging.L().Warnf("plugin %s: manifest declares no runtime — defaulting to %q (the sandbox); declare it explicitly", id, defaultManifestRuntime)
}

// ValidatePluginID is validatePluginID for callers outside this package —
// HTTP handlers that take a plugin id from the path (ut-docs#2891 M2).
func ValidatePluginID(id string) error { return validatePluginID(id) }

// ValidatePluginVersion is validatePluginVersion for callers outside this
// package — handlers that take a version from a body or query string.
func ValidatePluginVersion(v string) error { return validatePluginVersion(v) }
