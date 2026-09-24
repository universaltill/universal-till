package ui

import (
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// Manage-shop catalog contract §3.2 (migration 041): a category with
// show_on_sale_screen off (sell_screen_hidden = 1) is left out of the sale
// screen's category strip, tabs and overflow — with its subtree — while its
// items stay sellable (All tab, search, scan). Its buttons never fall into
// the uncategorised bucket instead.
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
	groups := BuildCategoryGroups(buttons, cats, nil)
	if len(groups) != 1 || groups[0].ID != "food" {
		t.Fatalf("groups = %+v, want only Food (Drinks and its subtree hidden, no uncategorised bucket)", groups)
	}
	if len(groups[0].Children) != 0 {
		t.Fatalf("Food children = %+v, want the hidden Dairy left out", groups[0].Children)
	}
	tiles := BuildCategoryTiles(buttons, cats)
	if len(tiles) != 1 || tiles[0].ID != "food" || tiles[0].ItemCount != 1 {
		t.Fatalf("tiles = %+v", tiles)
	}
}

// The category icon id renders only through the till's icon registry: a
// known id shows its artwork, an unknown or malformed one the neutral
// fallback, never the raw value; an explicit image wins over the icon.
func TestBuildCategoryGroups_IconIDRendersViaRegistry(t *testing.T) {
	cats := []data.CategoryNode{
		{ID: "a", Name: "A", Icon: "lucide:coffee"},
		{ID: "b", Name: "B", Icon: "tabler:not-on-this-till"},
		{ID: "c", Name: "C", Icon: "<img src=x onerror=alert(1)>"},
		{ID: "d", Name: "D", Icon: "lucide:coffee", ImagePath: "/public/assets/category-icons/pastry.svg"},
		{ID: "e", Name: "E"},
	}
	var buttons []Button
	for _, c := range cats {
		buttons = append(buttons, Button{Label: c.Name, Code: c.ID, ItemID: "i" + c.ID, CategoryID: c.ID})
	}
	want := map[string]string{
		"a": "/public/assets/category-icons/coffee.svg",
		"b": "/public/assets/category-icons/generic.svg",
		"c": "/public/assets/category-icons/generic.svg",
		"d": "/public/assets/category-icons/pastry.svg",
		"e": "",
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
		t.Fatal("an item of a hidden category must stay sellable from the All grid")
	}
	h.BrowsingMode = "category_tabs"
	body = renderList(t, h)
	if strings.Contains(body, "Drinks") {
		t.Fatalf("category_tabs still offers the hidden Drinks tile")
	}
}
