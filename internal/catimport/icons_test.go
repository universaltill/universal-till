package catimport

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func catimportRepoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
}

// TestIconRegistry_FirstCutCoverage pins the product owner's first-cut
// list on ut-docs#2506: each of these is its own icon, not a recoloured
// cup, and the library is big enough to be a library.
func TestIconRegistry_FirstCutCoverage(t *testing.T) {
	want := []string{
		"coffee", "espresso", "tea", "drink", "can", "water", "juice", "smoothie",
		"beer", "wine", "wine-bottle", "cocktail", "spirits", "champagne",
		"cake", "cake-slice", "cupcake", "croissant", "bread", "baguette", "cookie", "donut",
		"sandwich", "burger", "pizza", "hot-dog", "salad", "soup", "noodles", "breakfast",
		"cheese", "meat", "fish", "chicken",
		"apple", "banana", "citrus", "berries", "grapes", "carrot", "greens", "pepper",
		"ice-cream", "candy", "chocolate",
		"tag", "gift", "bag", "box", "star", "offer", "vegan", "gluten-free", "generic",
	}
	for _, k := range want {
		if _, ok := iconByKey[k]; !ok {
			t.Errorf("first-cut icon %q missing from the registry", k)
		}
	}
	if n := len(iconDefs); n < 60 {
		t.Errorf("library has %d icons, want at least 60", n)
	}
}

// TestIconRegistry_LegacyKeysStillResolve: shops already store these
// paths (ut-docs#1189 imports, #1844/#2500 picks); a rename would break
// their tiles.
func TestIconRegistry_LegacyKeysStillResolve(t *testing.T) {
	for _, k := range []string{"coffee", "drink", "sandwich", "pastry", "generic"} {
		p, ok := IconPath(k)
		if !ok || p != "/public/assets/category-icons/"+k+".svg" {
			t.Errorf("IconPath(%q) = (%q, %v), want the pre-#2506 path", k, p, ok)
		}
	}
}

// TestIconRegistry_Consistent: unique keys, a known group, a vendored
// source that exists, and no two icons drawn from the same source glyph
// (each icon must look different).
func TestIconRegistry_Consistent(t *testing.T) {
	root := catimportRepoRoot(t)
	groups := map[string]bool{}
	for _, g := range iconGroups {
		groups[g.Key] = true
	}
	keys, srcs := map[string]bool{}, map[string]string{}
	for _, d := range iconDefs {
		if keys[d.Key] {
			t.Errorf("duplicate key %q", d.Key)
		}
		keys[d.Key] = true
		if !groups[d.Group] {
			t.Errorf("%q: unknown group %q", d.Key, d.Group)
		}
		if len(d.Keywords) == 0 {
			t.Errorf("%q: no search keywords", d.Key)
		}
		if d.Src == "" {
			if d.Key != "generic" {
				t.Errorf("%q: only the legacy generic tile may be hand-drawn", d.Key)
			}
			continue
		}
		if prev, dup := srcs[d.Src]; dup {
			t.Errorf("%q and %q both draw %s — every icon must be distinct", prev, d.Key, d.Src)
		}
		srcs[d.Src] = d.Key
		if _, err := os.Stat(iconSourcePath(filepath.Join(root, "internal", "catimport", "iconsrc"), d.Src)); err != nil {
			t.Errorf("%q: vendored source missing: %v", d.Key, err)
		}
	}
	for _, set := range []string{"lucide", "tabler"} {
		if _, err := os.Stat(filepath.Join(root, "internal", "catimport", "iconsrc", set, "LICENSE")); err != nil {
			t.Errorf("iconsrc/%s/LICENSE missing — the upstream licence must ship with the SVGs: %v", set, err)
		}
	}
}

// TestCategoryIconTiles_InSync: every committed tile is exactly what the
// generator (icons_render_test.go) makes from its vendored source, and the
// directory holds no tile the registry doesn't know. Fix:
//
//	UPDATE_CATEGORY_ICONS=1 go test ./internal/catimport -run TestCategoryIconTiles_InSync
func TestCategoryIconTiles_InSync(t *testing.T) {
	root := catimportRepoRoot(t)
	update := os.Getenv("UPDATE_CATEGORY_ICONS") == "1"
	srcDir := filepath.Join(root, "internal", "catimport", "iconsrc")
	outDir := filepath.Join(root, "web", "public", "assets", "category-icons")
	for _, d := range iconDefs {
		tilePath := filepath.Join(outDir, d.Key+".svg")
		if d.Src == "" {
			// Hand-drawn legacy tile (generic): must exist, never generated.
			if _, err := os.Stat(tilePath); err != nil {
				t.Errorf("%q: tile missing: %v", d.Key, err)
			}
			continue
		}
		src, err := os.ReadFile(iconSourcePath(srcDir, d.Src))
		if err != nil {
			t.Errorf("%q: %v", d.Key, err)
			continue
		}
		want, err := renderIconTile(d, src)
		if err != nil {
			t.Errorf("%q: %v", d.Key, err)
			continue
		}
		if update {
			if err := os.WriteFile(tilePath, want, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		onDisk, err := os.ReadFile(tilePath)
		if err != nil {
			t.Errorf("%q: tile missing: %v", d.Key, err)
			continue
		}
		if !bytes.Equal(onDisk, want) {
			t.Errorf("%q: committed tile differs from the generator's output — regenerate with UPDATE_CATEGORY_ICONS=1 (see this test's doc comment)", d.Key)
		}
	}
	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if k := strings.TrimSuffix(e.Name(), ".svg"); iconByKey[k].Key == "" {
			t.Errorf("stray tile %s has no registry entry", e.Name())
		}
	}
}

// TestRenderIconTile_Shape: the tile is self-contained (no currentColor,
// which an <img> can't inherit), drops Tabler's invisible bounding path,
// and names its source + licence.
func TestRenderIconTile_Shape(t *testing.T) {
	src := []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" stroke="currentColor">
  <path stroke="none" d="M0 0h24v24H0z" fill="none" />
  <path d="M8 21h8" />
  <circle cx="7.5" cy="7.5" r=".5" fill="currentColor" />
</svg>`)
	out, err := renderIconTile(iconDef{Key: "x", Src: "tabler:x", Group: "bar"}, src)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, bad := range []string{"currentColor", `stroke="none"`} {
		if strings.Contains(s, bad) {
			t.Errorf("tile contains %q:\n%s", bad, s)
		}
	}
	for _, good := range []string{`viewBox="0 0 64 64"`, `<path d="M8 21h8"/>`, `fill="#5B21B6"`, "tabler:x", "iconsrc/tabler/LICENSE"} {
		if !strings.Contains(s, good) {
			t.Errorf("tile lacks %q:\n%s", good, s)
		}
	}
	if _, err := renderIconTile(iconDef{Key: "y", Src: "lucide:y", Group: "bar"}, []byte(`<svg></svg>`)); err == nil {
		t.Error("an empty source rendered without error — would ship a blank tile")
	}
}

// TestBuiltinIconGroups_CoverEveryIconOnce: the pickers render groups, so
// an icon outside every group would silently vanish from the UI.
func TestBuiltinIconGroups_CoverEveryIconOnce(t *testing.T) {
	seen := map[string]int{}
	for _, g := range BuiltinIconGroups() {
		if len(g.Icons) == 0 {
			t.Errorf("group %q is empty", g.Key)
		}
		if g.I18nKey != "catalog.builtin_icon.group."+g.Key {
			t.Errorf("group %q I18nKey = %q", g.Key, g.I18nKey)
		}
		for _, ic := range g.Icons {
			seen[ic.Key]++
		}
	}
	for _, ic := range BuiltinIcons() {
		if seen[ic.Key] != 1 {
			t.Errorf("icon %q appears in %d groups, want 1", ic.Key, seen[ic.Key])
		}
	}
}

// TestBuiltinIcons_LocaleKeysExist: every icon label and group heading is
// a key in en.json (guard-i18n.sh then checks every other locale has it).
func TestBuiltinIcons_LocaleKeysExist(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(catimportRepoRoot(t), "web", "locales", "en.json"))
	if err != nil {
		t.Fatal(err)
	}
	en := string(raw)
	check := func(k string) {
		if !strings.Contains(en, `"`+k+`"`) {
			t.Errorf("en.json lacks %q", k)
		}
	}
	for _, g := range BuiltinIconGroups() {
		check(g.I18nKey)
		for _, ic := range g.Icons {
			check(ic.I18nKey)
		}
	}
}
