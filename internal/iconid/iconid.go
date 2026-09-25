// Package iconid is the till's half of the category icon id contract
// (ut-docs reference/manage-shop-catalog-api.md §0.12): a category icon is
// an id string "namespace:name" such as "lucide:coffee" — never SVG, a URL
// or markup. ut-cloud checks an id against the full shared registry; the
// till checks the FORMAT only on write, and on the sale screen renders
// only the ids this registry maps to artwork the binary ships. Anything
// else — a well-formed id a newer cloud knows and this till doesn't, or a
// malformed value that arrived over LAN sync — renders the neutral
// Fallback glyph, never the raw value.
//
// The registry is the whole icon library (library.go, ut-docs#2506), one
// list shared with internal/catimport's picker (ut-docs#2664).
package iconid

import (
	"regexp"
)

// MaxLen is the longest id accepted, in bytes (contract §0.12).
const MaxLen = 64

// Fallback is the id rendered for an unknown icon.
const Fallback = "lucide:tag"

var formatRE = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*:[a-z0-9]+(-[a-z0-9]+)*$`)

// ValidFormat reports whether id is a well-formed icon id. "" (no icon) is
// NOT valid here: callers that accept "" for "none" check it themselves.
func ValidFormat(id string) bool {
	return len(id) <= MaxLen && formatRE.MatchString(id)
}

// registry maps each icon id this till can draw to its library tile, and
// libraryPaths maps every tile's path back to its id ("" for the id-less
// generic tile). Both are derived from library (library.go) — the one
// list (ut-docs#2664).
var registry, libraryPaths = func() (map[string]string, map[string]string) {
	reg := make(map[string]string, len(library))
	paths := make(map[string]string, len(library))
	for _, ic := range library {
		p := PublicDir + ic.Key + ".svg"
		paths[p] = ic.ID
		if ic.ID != "" {
			reg[ic.ID] = p
		}
	}
	return reg, paths
}()

// AssetPath returns the /public/... asset to render for a stored icon id:
// "" for no icon, the registered artwork for a known id, and the
// Fallback's artwork for anything else.
func AssetPath(id string) string {
	if id == "" {
		return ""
	}
	if p, ok := registry[id]; ok {
		return p
	}
	return registry[Fallback]
}

// IDForAssetPath returns the icon id of the library tile at path, or ""
// when path is not a library tile (an uploaded photo, anything else) or is
// the id-less generic tile. Older tills stored a library pick as its path
// in categories.image_path (ut-docs#2500); this is the read-time mapping
// that makes such a row an icon (ut-docs#2717).
func IDForAssetPath(path string) string {
	return libraryPaths[path]
}

// Resolve applies the one-picture rule (ut-docs#2717) to a category's two
// columns: path is an image to show — an uploaded photo, or the id-less
// generic tile — and id the icon to draw when path is "" or this till
// cannot serve it (a photo's file does not travel over LAN sync).
//
//   - A set icon beats a library tile. Writers now keep only one of the
//     two columns, so both being set means a row from before #2717: a
//     library pick on the till with a later icon from my. over it — the
//     pilot's case, where the newer icon must show.
//   - A library tile with no icon reads as the tile's own id.
//   - An uploaded photo wins over an icon (again a pre-#2717 row); the icon
//     stays the fallback for a till without the photo's file.
func Resolve(imagePath, icon string) (path, id string) {
	libID, isLib := libraryPaths[imagePath]
	switch {
	case isLib && icon != "":
		return "", icon
	case isLib && libID != "":
		return "", libID
	default:
		return imagePath, icon
	}
}

// EffectiveIcon is the icon id a category shows, "" when it shows a photo,
// the generic tile or nothing — what the till reports to the cloud so my.
// draws what the sale screen draws (ut-docs#2717).
func EffectiveIcon(imagePath, icon string) string {
	if path, id := Resolve(imagePath, icon); path == "" {
		return id
	}
	return ""
}
