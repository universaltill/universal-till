package plugins

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"github.com/universaltill/universal-till/internal/paths"
)

// BundleFont is one font file an active language plugin ships in its
// fonts/ directory (ADR-0160 §8, ut-docs#4374): a subset for a script the
// till doesn't ship (CJK, Indic, Thai…), served from the till's own disk —
// never a CDN (ADR-0003).
type BundleFont struct {
	PluginID string
	Version  string
	File     string // base name inside fonts/, always matching bundleFontName
}

// bundleFontName is the whole set of file names a bundle font may have:
// woff2 only (the format ADR-0160 §8 names), a plain name with no path,
// space or quote. The name lands in a URL inside generated CSS
// (/plugin-fonts/bundle-fonts.css), so this is also its escaping rule.
var bundleFontName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}\.woff2$`)

// IsBundleFontName reports whether name is a file name a bundle font may
// have (see bundleFontName).
func IsBundleFontName(name string) bool { return bundleFontName.MatchString(name) }

// BundleFonts returns the font files of every ACTIVE language plugin, as of
// the last Init/Reload, sorted by plugin id then file name. The slice is a
// copy. Nil-safe.
func (m *Manager) BundleFonts() []BundleFont {
	if m == nil {
		return nil
	}
	m.fontsMu.RLock()
	defer m.fontsMu.RUnlock()
	if len(m.fonts) == 0 {
		return nil
	}
	return append([]BundleFont(nil), m.fonts...)
}

// syncBundleFonts rebuilds the font set from disk. Only a plugin whose
// on-disk manifest (verified at install) says canonical_type "language"
// contributes: a font joins the till's global font stack, so any other
// plugin type has no business shipping one there. Within fonts/, only
// regular files whose name passes IsBundleFontName count; anything else is
// ignored, not served, and so is a file without the woff2 signature.
func (m *Manager) syncBundleFonts() {
	var fonts []BundleFont
	for _, id := range m.InstalledIDs() { // active plugins only, sorted by id
		p := m.Installed[id]
		if ValidatePluginID(id) != nil || ValidatePluginVersion(p.Version) != nil {
			continue
		}
		base := filepath.Join(paths.Plugins(), id, p.Version)
		if !isLanguagePlugin(filepath.Join(base, "manifest.json")) {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(base, "fonts"))
		if err != nil {
			continue
		}
		for _, e := range entries { // ReadDir sorts by file name
			if !e.Type().IsRegular() || !IsBundleFontName(e.Name()) || !hasWOFF2Signature(filepath.Join(base, "fonts", e.Name())) {
				continue
			}
			fonts = append(fonts, BundleFont{PluginID: id, Version: p.Version, File: e.Name()})
		}
	}
	m.fontsMu.Lock()
	m.fonts = fonts
	m.fontsMu.Unlock()
}

// hasWOFF2Signature reports whether the file starts with the woff2 magic
// "wOF2". A file merely named *.woff2 is never handed to the browser's font
// parser on every page (fail closed).
func hasWOFF2Signature(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var magic [4]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil {
		return false
	}
	return string(magic[:]) == "wOF2"
}

func isLanguagePlugin(manifestPath string) bool {
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return false
	}
	var mf struct {
		CanonicalType string `json:"canonical_type"`
	}
	return json.Unmarshal(raw, &mf) == nil && mf.CanonicalType == "language"
}
