package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/paths"
)

// ut-docs#2500: a category's image (a built-in icon or an uploaded photo)
// shows on its sell-screen tile, strip tab and overflow tile. The column
// syncs to satellites but an uploaded FILE does not (the D2 limit), so the
// one resolver keeps a built-in icon, keeps an uploaded path only when the
// file exists on this till, and otherwise answers "no image" — a name-only
// tile, never a broken <img>.
func TestCategoryImageURL_ResolvesBuiltinUploadAndMissing(t *testing.T) {
	orig := paths.DataDir()
	dir := t.TempDir()
	paths.Init(dir)
	t.Cleanup(func() { paths.Init(orig) })

	const builtin = "/public/assets/category-icons/coffee.svg"
	const uploaded = "/public/assets/categories/cat1/thumb.png"
	cases := map[string]string{
		"":                    "",
		builtin:               builtin,
		uploaded:              "", // file not on this till (a satellite)
		"javascript:alert(1)": "",
		"/etc/passwd":         "",
	}
	for in, want := range cases {
		if got := categoryImageURL(in); got != want {
			t.Errorf("categoryImageURL(%q) = %q, want %q", in, got, want)
		}
	}

	// Once the uploaded file exists in the data dir, it is kept.
	p := filepath.Join(dir, "public", "assets", "categories", "cat1", "thumb.png")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := categoryImageURL(uploaded); got != uploaded {
		t.Fatalf("categoryImageURL(present upload) = %q, want %q", got, uploaded)
	}
}

func TestBuildCategoryGroupsAndTiles_CarryImageURL(t *testing.T) {
	cats := []data.CategoryNode{
		{ID: "coffee", Name: "Coffee", ImagePath: "/public/assets/category-icons/coffee.svg"},
		{ID: "cakes", Name: "Cakes", ImagePath: "/public/assets/categories/cakes-missing/thumb.png"},
		{ID: "plain", Name: "Plain"},
	}
	buttons := []Button{
		{Label: "Latte", Code: "L", ItemID: "i1", CategoryID: "coffee"},
		{Label: "Scone", Code: "S", ItemID: "i2", CategoryID: "cakes"},
		{Label: "Water", Code: "W", ItemID: "i3", CategoryID: "plain"},
	}
	want := map[string]string{"coffee": "/public/assets/category-icons/coffee.svg", "cakes": "", "plain": ""}
	for _, g := range BuildCategoryGroups(buttons, cats, nil) {
		if g.ImageURL != want[g.ID] {
			t.Errorf("group %s ImageURL = %q, want %q", g.ID, g.ImageURL, want[g.ID])
		}
	}
	for _, tile := range BuildCategoryTiles(buttons, cats) {
		if tile.ImageURL != want[tile.ID] {
			t.Errorf("tile %s ImageURL = %q, want %q", tile.ID, tile.ImageURL, want[tile.ID])
		}
	}
}

// The three category spots render an <img> only when the category has
// one — alt="" because the visible name already labels the tile.
func TestButtonsHTTPList_CategoryImageRendersOnlyWhenSet(t *testing.T) {
	db, _, h := newBrowsingModeTestHTTP(t)
	mustExec(t, db, `UPDATE categories SET image_path = '/public/assets/category-icons/pastry.svg' WHERE id = 'cat_food'`)

	h.BrowsingMode = "category_tabs"
	body := renderList(t, h)
	if got := strings.Count(body, `class="category-tile-img"`); got != 1 {
		t.Fatalf("category_tabs: want exactly 1 category-tile-img (Food), got %d: %s", got, body)
	}
	mustContainAll(t, body, `src="/public/assets/category-icons/pastry.svg?v=`, `alt=""`)

	h.BrowsingMode = "strip_overflow"
	body = renderList(t, h)
	if got := strings.Count(body, `class="tab-cat-img"`); got != 1 {
		t.Fatalf("strip: want exactly 1 tab-cat-img (Food's tab), got %d: %s", got, body)
	}
	if got := strings.Count(body, `class="category-tile-img"`); got != 1 {
		t.Fatalf("strip overflow sheet: want exactly 1 category-tile-img (Food), got %d: %s", got, body)
	}
}
