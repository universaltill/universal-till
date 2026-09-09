package catimport

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestPlaceholderIcon_KeywordMatch pins the Phase 1 scope of ut-docs#1189:
// an imported item with no source image gets a sensible generic category
// icon, chosen by keyword match against name/category — not a blank tile.
func TestPlaceholderIcon_KeywordMatch(t *testing.T) {
	cases := []struct {
		name, category, want string
	}{
		{"Cappuccino", "Hot Drinks", "coffee"},
		{"Chai Latte", "", "coffee"},
		{"Bagel", "", "pastry"},
		{"Croissant", "Bakery", "pastry"},
		{"Club Sandwich", "Food", "sandwich"},
		{"Coca-Cola 330ml", "Drinks", "drink"},
		{"Bananas", "Produce", "generic"},
		{"", "", "generic"},
	}
	for _, c := range cases {
		if got := PlaceholderIcon(c.name, c.category); got != c.want {
			t.Errorf("PlaceholderIcon(%q, %q) = %q, want %q", c.name, c.category, got, c.want)
		}
	}
}

// TestPlaceholderIcon_WholeWordOnly is review finding F5 (ut-docs#1189): a
// first-draft substring match matched "tea" inside "s-TEA-k" and "cola"
// inside "cho-COLA-te", silently mis-tagging an item that shares no real
// word with any keyword. Matching must be whole-word, not substring.
func TestPlaceholderIcon_WholeWordOnly(t *testing.T) {
	cases := []struct {
		name, category, want string
	}{
		{"Steak Sandwich", "", "sandwich"}, // "tea" inside "Steak" must not win
		{"Steak Bake", "", "generic"},      // no whole-word keyword match at all
		{"Chocolate Bar", "", "generic"},   // "cola" inside "Chocolate" must not win
	}
	for _, c := range cases {
		if got := PlaceholderIcon(c.name, c.category); got != c.want {
			t.Errorf("PlaceholderIcon(%q, %q) = %q, want %q (substring false-positive?)", c.name, c.category, got, c.want)
		}
	}
}

// TestPlaceholderIcon_CategoryOutranksAmbiguousName: a name that matches no
// keyword still resolves off the category when the category itself is
// recognised, rather than falling straight to generic.
func TestPlaceholderIcon_CategoryOutranksAmbiguousName(t *testing.T) {
	if got := PlaceholderIcon("House Blend No. 4", "Coffee"); got != "coffee" {
		t.Errorf("got %q, want coffee", got)
	}
}

// TestPlaceholderIconPath_ReturnsPublicAssetPath mirrors the
// "/public/assets/items/<id>/thumb.png" convention already used for
// per-item uploads (catalog/handlers.go) so the same imgv/ImageURL
// machinery resolves either kind of path unchanged.
func TestPlaceholderIconPath_ReturnsPublicAssetPath(t *testing.T) {
	got := PlaceholderIconPath("Cappuccino", "Hot Drinks")
	want := "/public/assets/category-icons/coffee.svg"
	if got != want {
		t.Errorf("PlaceholderIconPath = %q, want %q", got, want)
	}
}

// TestPlaceholderIcon_AllIconsKnown guards against a keyword table entry
// that points at an icon key with no matching case in
// PlaceholderIconPath's switch — a silent typo there would 404 forever
// rather than fail loudly.
//
// Review finding F4 (ut-docs#1189): an earlier version of this test only
// checked that iconPath returned SOME non-empty string, which would still
// pass even if iconPath's switch had typo'd "coffee.svg" as "cofee.svg" —
// wrong, but non-empty. Actually stat the bundled SVG on disk for every
// known icon key, so a typo here fails the build, not a runtime <img>.
func TestPlaceholderIcon_AllIconsKnown(t *testing.T) {
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))

	seen := map[string]bool{"generic": true}
	for _, kw := range keywordIcons {
		seen[kw.icon] = true
	}
	for icon := range seen {
		path, ok := IconPath(icon)
		if !ok || path == "" {
			t.Errorf("icon key %q has no known asset path", icon)
			continue
		}
		// path is a "/public/..." URL; the bundled asset lives at
		// web/<that path>, same convention statAsset (internal/httpx)
		// resolves for the built-in/embedded default tier.
		onDisk := filepath.Join(repoRoot, "web", filepath.FromSlash(path))
		if _, err := os.Stat(onDisk); err != nil {
			t.Errorf("icon key %q → %q, but no file at %s: %v", icon, path, onDisk, err)
		}
	}
}

// TestIconPath_UnknownKeyIsNotOK: the picker (ut-docs#1844) trusts this ok
// flag to reject a bad/forged icon key from a client request — an empty
// string with no ok=false would be indistinguishable from a genuine (if
// oddly empty) path.
func TestIconPath_UnknownKeyIsNotOK(t *testing.T) {
	if path, ok := IconPath("not-a-real-icon"); ok || path != "" {
		t.Errorf("IconPath(bogus) = (%q, %v), want (\"\", false)", path, ok)
	}
}

// TestBuiltinIcons_MatchesIconPath (ut-docs#1844): the picker UI enumerates
// BuiltinIcons() to render its grid, and separately validates a submitted
// choice via IconPath — the two must agree on every key, or a tile the UI
// happily renders could be rejected by the server that's supposed to
// accept it back.
func TestBuiltinIcons_MatchesIconPath(t *testing.T) {
	icons := BuiltinIcons()
	if len(icons) == 0 {
		t.Fatal("BuiltinIcons() returned none")
	}
	seenKeys := map[string]bool{}
	for _, ic := range icons {
		if ic.Key == "" {
			t.Errorf("BuiltinIcons() entry with empty Key: %+v", ic)
		}
		if ic.I18nKey == "" {
			t.Errorf("BuiltinIcons() entry %q has no I18nKey", ic.Key)
		}
		if seenKeys[ic.Key] {
			t.Errorf("BuiltinIcons() duplicate key %q", ic.Key)
		}
		seenKeys[ic.Key] = true
		wantPath, ok := IconPath(ic.Key)
		if !ok {
			t.Errorf("BuiltinIcons() key %q not recognised by IconPath", ic.Key)
			continue
		}
		if ic.Path != wantPath {
			t.Errorf("BuiltinIcons() key %q Path = %q, want %q (from IconPath)", ic.Key, ic.Path, wantPath)
		}
	}
}
