package ui

import (
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// Manage-shop catalog contract §3.2 (migration 041): a category with
// show_on_sale_screen off (sell_screen_hidden = 1) is left out of the sale
// screen's category strip, tabs and overflow — with its subtree — while its
// items stay sellable by search, scan AND quick buttons: a quick button
// attached to a hidden category (or to one of its subcategories) moves to
// the uncategorised bucket, never off the sale screen (review finding 2).
func TestBuildCategoryGroups_HiddenCategoryLeftOut(t *testing.T) {
	cats := []data.CategoryNode{
		{ID: "food", Name: "Food"},
		{ID: "dairy", Name: "Dairy", ParentID: "food", SellScreenHidden: true},
		{ID: "drink", Name: "Drinks", SellScreenHidden: true},
		{ID: "hot", Name: "Hot", ParentID: "drink"},
	}
	buttons := []Button{
		{Label: "Bread", Code: "B", ItemID: "i1", CategoryID: "food"},
		{Label: "Butter", Code: "U", ItemID: "i2", CategoryID: "dairy"},
		{Label: "Cola", Code: "C", ItemID: "i3", CategoryID: "drink"},
		{Label: "Tea", Code: "T", ItemID: "i4", CategoryID: "hot"},
	}
	// The same catalog as explicit quick buttons, plus an implicit tile
	// (Juice) in the hidden Drinks: only the quick buttons move.
	var quick []Button
	for _, b := range buttons {
		b.QuickButton = true
		quick = append(quick, b)
	}
	quick = append(quick, Button{Label: "Juice", Code: "J", ItemID: "i5", CategoryID: "drink"})
	groups := BuildCategoryGroups(quick, cats, nil)
	if len(groups) != 2 || groups[0].ID != "food" || groups[1].ID != "" {
		t.Fatalf("groups = %+v, want Food then the uncategorised bucket (Drinks and its subtree hidden)", groups)
	}
	if len(groups[0].Children) != 0 {
		t.Fatalf("Food children = %+v, want the hidden Dairy left out", groups[0].Children)
	}
	var moved []string
	for _, b := range groups[1].Buttons {
		moved = append(moved, b.Label)
	}
	if strings.Join(moved, ",") != "Butter,Cola,Tea" {
		t.Fatalf("uncategorised bucket = %v, want the hidden categories' quick buttons in their global order and no implicit tile", moved)
	}
	tiles := BuildCategoryTiles(buttons, cats)
	if len(tiles) != 1 || tiles[0].ID != "food" || tiles[0].ItemCount != 1 {
		t.Fatalf("tiles = %+v", tiles)
	}
}

// The category icon id renders only through the till's icon registry: a
// known id shows its artwork, an unknown or malformed one the neutral
// fallback, never the raw value. ut-docs#2717: a set icon beats a library
// tile left in image_path (the pilot's rows — "d": the till's #2500 pastry
// pick under a newer my. coffee icon, which used to stay hidden), and a
// library tile with no icon reads as its own id ("f").
func TestBuildCategoryGroups_IconIDRendersViaRegistry(t *testing.T) {
	cats := []data.CategoryNode{
		{ID: "a", Name: "A", Icon: "lucide:coffee"},
		{ID: "b", Name: "B", Icon: "tabler:not-on-this-till"},
		{ID: "c", Name: "C", Icon: "<img src=x onerror=alert(1)>"},
		{ID: "d", Name: "D", Icon: "lucide:coffee", ImagePath: "/public/assets/category-icons/pastry.svg"},
		{ID: "e", Name: "E"},
		{ID: "f", Name: "F", ImagePath: "/public/assets/category-icons/beer.svg"},
	}
	var buttons []Button
	for _, c := range cats {
		buttons = append(buttons, Button{Label: c.Name, Code: c.ID, ItemID: "i" + c.ID, CategoryID: c.ID})
	}
	want := map[string]string{
		"a": "/public/assets/category-icons/coffee.svg",
		"b": "/public/assets/category-icons/tag.svg",
		"c": "/public/assets/category-icons/tag.svg",
		"d": "/public/assets/category-icons/coffee.svg",
		"e": "",
		"f": "/public/assets/category-icons/beer.svg",
	}
	for _, g := range BuildCategoryGroups(buttons, cats, nil) {
		if g.ImageURL != want[g.ID] {
			t.Errorf("group %s ImageURL = %q, want %q", g.ID, g.ImageURL, want[g.ID])
		}
	}
}

func TestButtonsHTTPList_HiddenCategoryHasNoTabButItemsStayInAll(t *testing.T) {
	db, _, h := newBrowsingModeTestHTTP(t)
	mustExec(t, db, `UPDATE categories SET sell_screen_hidden = 1 WHERE id = 'cat_drink'`)
	h.BrowsingMode = "strip_overflow"
	body := renderList(t, h)
	if strings.Contains(body, `id="cat-tab-cat_drink"`) {
		t.Fatal("the hidden Drinks category still has a strip tab")
	}
	if !strings.Contains(body, `id="cat-tab-cat_food"`) {
		t.Fatal("a visible category lost its tab")
	}
	if !strings.Contains(body, "Cola") {
		t.Fatal("a quick button of a hidden category must stay on the strip (uncategorised tab)")
	}
	h.BrowsingMode = "category_tabs"
	body = renderList(t, h)
	if strings.Contains(body, "Drinks") {
		t.Fatalf("category_tabs still offers the hidden Drinks tile")
	}
}

// Review finding 2: the strip has no All tab (ut-docs#2613), so a quick
// button whose category is hidden must still be on the sale screen (in the
// uncategorised bucket).
func TestButtonsHTTPList_HiddenCategoryQuickButtonStaysReachable(t *testing.T) {
	db, _, h := newBrowsingModeTestHTTP(t)
	mustExec(t, db, `UPDATE categories SET sell_screen_hidden = 1 WHERE id = 'cat_drink'`)
	h.BrowsingMode = "strip_overflow"
	body := renderList(t, h)
	if strings.Contains(body, `id="cat-tab-cat_drink"`) {
		t.Fatal("the hidden Drinks category still has a strip tab")
	}
	if !strings.Contains(body, `data-code="C_COLA"`) {
		t.Fatal("the Cola quick button of the hidden Drinks category left the sale screen")
	}
	if !strings.Contains(body, `id="cat-tab-uncategorized"`) {
		t.Fatal("want the uncategorised tab carrying the moved quick button")
	}
}
