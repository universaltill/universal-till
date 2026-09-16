package ui

import (
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// TestTopLevelCategoryTiles_OneTilePerTopLevelCategory (ut-docs#2283): the
// Categories tab's tile grid is flat, top-level only — a subcategory never
// gets its own tile, mirroring category_filter.html's existing
// `{{ if not .ParentID }}` convention.
func TestTopLevelCategoryTiles_OneTilePerTopLevelCategory(t *testing.T) {
	cats := []data.CategoryNode{
		{ID: "drinks", Name: "Drinks", Color: "#1D4ED8"},
		{ID: "hot-drinks", Name: "Hot Drinks", ParentID: "drinks"},
		{ID: "food", Name: "Food"},
	}
	buttons := []Button{
		{Label: "Latte", Code: "L1", ItemID: "i1", CategoryID: "hot-drinks"},
		{Label: "Burger", Code: "B1", ItemID: "i2", CategoryID: "food"},
	}

	tiles := TopLevelCategoryTiles(buttons, cats)
	if len(tiles) != 2 {
		t.Fatalf("len(tiles) = %d, want 2 (one per top-level category), got %+v", len(tiles), tiles)
	}
	byID := map[string]CategoryTile{}
	for _, tl := range tiles {
		byID[tl.ID] = tl
	}
	if _, ok := byID["hot-drinks"]; ok {
		t.Fatalf("a nested subcategory must never get its own tile, got %+v", tiles)
	}
	drinks, ok := byID["drinks"]
	if !ok {
		t.Fatalf("expected a Drinks tile (has an active button via its child Hot Drinks), got %+v", tiles)
	}
	if drinks.Name != "Drinks" || drinks.Color != "#1D4ED8" {
		t.Fatalf("unexpected Drinks tile: %+v", drinks)
	}
	if _, ok := byID["food"]; !ok {
		t.Fatalf("expected a Food tile, got %+v", tiles)
	}
}

// TestTopLevelCategoryTiles_CountsDescendantButtons: a top-level category
// with no DIRECT buttons of its own, but at least one active button
// somewhere in its subtree (a grandchild included), still gets a tile —
// mirrors pruneEmptyCategoryGroup's "no buttons anywhere in this branch"
// test, just counted across the whole top-level subtree instead of pruning
// a nested tree.
func TestTopLevelCategoryTiles_CountsDescendantButtons(t *testing.T) {
	cats := []data.CategoryNode{
		{ID: "drinks", Name: "Drinks"},
		{ID: "hot", Name: "Hot", ParentID: "drinks"},
		{ID: "tea", Name: "Tea", ParentID: "hot"}, // grandchild of drinks
	}
	buttons := []Button{
		{Label: "Green Tea", Code: "T1", ItemID: "i1", CategoryID: "tea"},
	}

	tiles := TopLevelCategoryTiles(buttons, cats)
	if len(tiles) != 1 || tiles[0].ID != "drinks" {
		t.Fatalf("expected exactly one Drinks tile counting its grandchild's button, got %+v", tiles)
	}
}

// TestTopLevelCategoryTiles_ExcludesZeroActiveCategories: a top-level
// category with zero active buttons anywhere in its subtree must not
// appear as a tile at all.
func TestTopLevelCategoryTiles_ExcludesZeroActiveCategories(t *testing.T) {
	cats := []data.CategoryNode{
		{ID: "drinks", Name: "Drinks"},
		{ID: "empty", Name: "Nothing Here"},
		{ID: "empty-child", Name: "Also Nothing", ParentID: "empty"},
	}
	buttons := []Button{
		{Label: "Cola", Code: "C1", ItemID: "i1", CategoryID: "drinks"},
	}

	tiles := TopLevelCategoryTiles(buttons, cats)
	if len(tiles) != 1 || tiles[0].ID != "drinks" {
		t.Fatalf("expected only Drinks (Nothing Here has zero active buttons anywhere), got %+v", tiles)
	}
}

// TestTopLevelCategoryTiles_NoCategories: nothing configured at all -> no
// tiles, no panic.
func TestTopLevelCategoryTiles_NoCategories(t *testing.T) {
	tiles := TopLevelCategoryTiles(nil, nil)
	if len(tiles) != 0 {
		t.Fatalf("expected no tiles with no categories, got %+v", tiles)
	}
}

// TestTopLevelCategoryTiles_CyclicParentChainNeverPanics: a malformed
// ParentID chain that loops back to itself (same defensive posture as
// isCategoryAncestor's own test) must never infinite-loop or panic —
// mirrors BuildCategoryGroups's own cycle defense.
func TestTopLevelCategoryTiles_CyclicParentChainNeverPanics(t *testing.T) {
	cats := []data.CategoryNode{
		{ID: "a", Name: "A", ParentID: "b"},
		{ID: "b", Name: "B", ParentID: "a"},
	}
	buttons := []Button{
		{Label: "X", Code: "X1", ItemID: "i1", CategoryID: "a"},
	}
	// Both a and b have a non-empty ParentID, so neither is top-level —
	// this must simply return no tiles, not hang or panic.
	tiles := TopLevelCategoryTiles(buttons, cats)
	if len(tiles) != 0 {
		t.Fatalf("expected no tiles for an all-cyclic category set, got %+v", tiles)
	}
}
