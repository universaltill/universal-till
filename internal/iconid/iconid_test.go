package iconid

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidFormat(t *testing.T) {
	for _, ok := range []string{"lucide:coffee", "tabler:glass-cocktail", "a:b", "x1:y-2-z"} {
		if !ValidFormat(ok) {
			t.Errorf("ValidFormat(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{
		"", "coffee", ":coffee", "lucide:", "Lucide:coffee", "lucide:Coffee",
		"lucide:-coffee", "lucide:coffee-", "lucide:cof--fee", "lucide:coffee:x",
		"lucide:cof fee", "<svg>", "https://x/y.svg", "/public/a.svg", "lucide:coffee\n",
		"a:" + strings.Repeat("b", 63), // 65 bytes
	} {
		if ValidFormat(bad) {
			t.Errorf("ValidFormat(%q) = true, want false", bad)
		}
	}
	if !ValidFormat("a:" + strings.Repeat("b", 62)) { // exactly 64 bytes
		t.Error("a 64-byte id must be valid")
	}
}

// The sale screen renders only ids the till's registry knows; anything
// else — unknown but well-formed, or malformed (the column rides the LAN
// sync and the cloud directive, so it is untrusted) — renders the neutral
// fallback, never the raw value.
func TestAssetPath(t *testing.T) {
	if got := AssetPath(""); got != "" {
		t.Fatalf("no icon must render nothing, got %q", got)
	}
	fallback := AssetPath(Fallback)
	if fallback == "" || !strings.HasPrefix(fallback, "/public/assets/category-icons/") {
		t.Fatalf("fallback path = %q", fallback)
	}
	if got := AssetPath("lucide:coffee"); got != "/public/assets/category-icons/coffee.svg" {
		t.Fatalf("lucide:coffee → %q", got)
	}
	for _, id := range []string{"tabler:unknown-thing", "lucide:not-in-the-library", "not an id", "<svg onload=x>", "../../etc"} {
		if got := AssetPath(id); got != fallback {
			t.Errorf("AssetPath(%q) = %q, want the fallback %q", id, got, fallback)
		}
	}
}

// Every registry entry must be a well-formed id that resolves to a bundled
// built-in icon file, so the registry can never point the sale screen at a
// missing asset.
func TestRegistryEntriesResolve(t *testing.T) {
	if len(Known()) == 0 {
		t.Fatal("registry is empty")
	}
	for _, id := range Known() {
		if !ValidFormat(id) {
			t.Errorf("registry id %q is malformed", id)
		}
		if !Registered(id) {
			t.Errorf("Known() lists %q but Registered says no", id)
		}
	}
	if !Registered(Fallback) {
		t.Fatal("the fallback id must itself be registered")
	}
}

// Each registered id's asset must ship in the web tree the binary embeds.
func TestRegistryAssetsExist(t *testing.T) {
	for _, id := range Known() {
		p := AssetPath(id)
		if _, err := os.Stat(filepath.Join("..", "..", "web", strings.TrimPrefix(p, "/"))); err != nil {
			t.Errorf("%s → %s: %v", id, p, err)
		}
	}
}

// ut-docs#5124: Picture is the single item-picture rule shared by the tile,
// the basket line and the item edit.
func TestPicture(t *testing.T) {
	beer := AssetPath("lucide:beer")
	if beer == "" {
		t.Fatal("registry lacks lucide:beer")
	}
	cases := []struct{ name, path, icon, want string }{
		{"none", "", "", ""},
		{"icon only", "", "lucide:beer", beer},
		{"photo wins", "/public/assets/items/x/thumb.png", "lucide:beer", "/public/assets/items/x/thumb.png"},
		{"unknown id falls back, never raw", "", "evil:<script>", AssetPath(Fallback)},
	}
	for _, c := range cases {
		if got := Picture(c.path, c.icon); got != c.want {
			t.Errorf("%s: Picture(%q,%q) = %q, want %q", c.name, c.path, c.icon, got, c.want)
		}
	}
}
