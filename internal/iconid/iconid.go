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
// The registry is deliberately a small seed: the full icon library, its
// shop-type groups and artwork are ut-docs#2506, which swaps the data in
// registry below without changing this package's API.
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

// registry maps each icon id this till can draw to its bundled asset (the
// catalog's built-in category icons, web/public/assets/category-icons/).
// Seed ids from the contract with no artwork here yet (lucide:cake-slice,
// lucide:egg-fried, lucide:soup, lucide:leaf) are simply absent: they
// render the fallback until ut-docs#2506 ships their glyphs.
var registry = map[string]string{
	"lucide:coffee":    "/public/assets/category-icons/coffee.svg",
	"lucide:cup-soda":  "/public/assets/category-icons/drink.svg",
	"lucide:croissant": "/public/assets/category-icons/pastry.svg",
	"lucide:sandwich":  "/public/assets/category-icons/sandwich.svg",
	"lucide:tag":       "/public/assets/category-icons/generic.svg",
}

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
