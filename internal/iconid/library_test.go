package iconid

import (
	"bufio"
	"os"
	"strings"
	"testing"
)

// ut-docs#2717 / #2664: one registry. Every library tile that is drawn
// from an upstream glyph carries that glyph's id, and the id resolves to
// exactly that tile.
func TestLibraryIDsResolveToTheirTile(t *testing.T) {
	seen := map[string]string{}
	withID := 0
	for _, ic := range Library() {
		if ic.ID == "" {
			if ic.Key != "generic" {
				t.Errorf("%q has no icon id — only the hand-drawn generic tile may lack one", ic.Key)
			}
			continue
		}
		withID++
		if prev, dup := seen[ic.ID]; dup {
			t.Errorf("%q and %q share id %s", prev, ic.Key, ic.ID)
		}
		seen[ic.ID] = ic.Key
		if !ValidFormat(ic.ID) {
			t.Errorf("%q: malformed id %q", ic.Key, ic.ID)
		}
		if got, want := AssetPath(ic.ID), PublicDir+ic.Key+".svg"; got != want {
			t.Errorf("AssetPath(%q) = %q, want %q", ic.ID, got, want)
		}
	}
	if withID < 60 {
		t.Fatalf("only %d library icons carry an id — the registry must be the whole library, not a seed", withID)
	}
	if len(Known()) != withID {
		t.Fatalf("registry has %d ids, library has %d — they must be one list", len(Known()), withID)
	}
}

// Every id my.'s category icon picker offers must draw real artwork on the
// till, never the fallback tag (the pilot's lucide:egg-fried and
// lucide:leaf did, ut-docs#2717). The list is a checked-in copy of the my.
// picker (testdata/my-shop-picker-ids.txt names its source file).
func TestMyShopPickerIDsResolve(t *testing.T) {
	f, err := os.Open("testdata/my-shop-picker-ids.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fallback := AssetPath(Fallback)
	n := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		id := strings.TrimSpace(sc.Text())
		if id == "" || strings.HasPrefix(id, "#") {
			continue
		}
		n++
		if !Registered(id) {
			t.Errorf("my. offers %s but the till has no artwork for it", id)
			continue
		}
		if id != Fallback && AssetPath(id) == fallback {
			t.Errorf("my. offers %s but the till draws the fallback for it", id)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if n < 8 {
		t.Fatalf("read only %d ids from the fixture", n)
	}
}

func TestIDForAssetPath(t *testing.T) {
	for path, want := range map[string]string{
		PublicDir + "coffee.svg":                 "lucide:coffee",
		PublicDir + "espresso.svg":               "tabler:coffee",
		PublicDir + "beer.svg":                   "lucide:beer",
		PublicDir + "generic.svg":                "", // hand-drawn, no upstream id
		PublicDir + "nope.svg":                   "",
		"/public/assets/categories/c1/thumb.png": "",
		"":                                       "",
		"/public/assets/category-icons/../x/beer.svg": "",
	} {
		if got := IDForAssetPath(path); got != want {
			t.Errorf("IDForAssetPath(%q) = %q, want %q", path, got, want)
		}
	}
}

// Resolve is the one rule for which of a category's two picture columns
// it shows (ut-docs#2717): an uploaded photo wins; a library tile stored
// by an older till reads as its icon id; a set icon beats a library tile
// (the pilot's rows: a #2500 library image under a newer my. icon).
func TestResolve(t *testing.T) {
	photo := "/public/assets/categories/c1/thumb.png"
	cases := []struct {
		name, imagePath, icon string
		wantPath, wantID      string
	}{
		{"nothing", "", "", "", ""},
		{"icon only", "", "lucide:soup", "", "lucide:soup"},
		{"photo only", photo, "", photo, ""},
		{"photo and a legacy icon: photo shows, icon is its fallback", photo, "lucide:soup", photo, "lucide:soup"},
		{"library tile maps to its id", PublicDir + "beer.svg", "", "", "lucide:beer"},
		{"icon beats a library tile", PublicDir + "beer.svg", "lucide:coffee", "", "lucide:coffee"},
		{"icon beats the generic tile", PublicDir + "generic.svg", "lucide:coffee", "", "lucide:coffee"},
		{"generic tile has no id: stays a path", PublicDir + "generic.svg", "", PublicDir + "generic.svg", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, id := Resolve(c.imagePath, c.icon)
			if p != c.wantPath || id != c.wantID {
				t.Fatalf("Resolve(%q, %q) = (%q, %q), want (%q, %q)", c.imagePath, c.icon, p, id, c.wantPath, c.wantID)
			}
		})
	}
}

// EffectiveIcon is what the cloud snapshot reports: the icon the till
// draws, or "" when it draws a photo (or nothing, or the id-less generic
// tile) — so my. shows what the till shows.
func TestEffectiveIcon(t *testing.T) {
	photo := "/public/assets/categories/c1/thumb.png"
	for _, c := range []struct{ imagePath, icon, want string }{
		{"", "", ""},
		{"", "lucide:soup", "lucide:soup"},
		{PublicDir + "beer.svg", "", "lucide:beer"},
		{PublicDir + "beer.svg", "lucide:coffee", "lucide:coffee"},
		{photo, "lucide:soup", ""},
		{photo, "", ""},
		{PublicDir + "generic.svg", "", ""},
	} {
		if got := EffectiveIcon(c.imagePath, c.icon); got != c.want {
			t.Errorf("EffectiveIcon(%q, %q) = %q, want %q", c.imagePath, c.icon, got, c.want)
		}
	}
}
