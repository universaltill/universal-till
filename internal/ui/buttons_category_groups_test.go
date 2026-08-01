package ui

import (
	"regexp"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

var hexColorPattern = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// TestBuildCategoryGroups_NestsByParentID pins the core "deep category
// trees, not a flat list" requirement: a child category's buttons must
// appear nested under its parent's group, not siblings in a flat list.
func TestBuildCategoryGroups_NestsByParentID(t *testing.T) {
	cats := []data.CategoryNode{
		{ID: "drinks", Name: "Drinks", SortOrder: 0},
		{ID: "hot-drinks", Name: "Hot Drinks", ParentID: "drinks", SortOrder: 0, Color: "#1D4ED8"},
	}
	buttons := []Button{
		{Label: "Latte", Code: "L1", ItemID: "i1", CategoryID: "hot-drinks"},
	}

	groups := BuildCategoryGroups(buttons, cats)
	if len(groups) != 1 {
		t.Fatalf("len(groups) = %d, want 1 root: %+v", len(groups), groups)
	}
	root := groups[0]
	if root.ID != "drinks" || len(root.Buttons) != 0 {
		t.Fatalf("unexpected root: %+v", root)
	}
	if len(root.Children) != 1 || root.Children[0].ID != "hot-drinks" {
		t.Fatalf("expected Hot Drinks nested under Drinks, got %+v", root.Children)
	}
	child := root.Children[0]
	if len(child.Buttons) != 1 || child.Buttons[0].Label != "Latte" {
		t.Fatalf("expected Latte under Hot Drinks, got %+v", child.Buttons)
	}
	if child.Color != "#1D4ED8" {
		t.Fatalf("expected explicit color preserved, got %q", child.Color)
	}
}

// TestBuildCategoryGroups_PrunesEmptyBranches: a category (and its empty
// subtree) with no buttons anywhere underneath must not appear at all —
// otherwise every category ever imported would show as an empty header on
// the till, even ones nothing is shortcut-mapped to.
func TestBuildCategoryGroups_PrunesEmptyBranches(t *testing.T) {
	cats := []data.CategoryNode{
		{ID: "drinks", Name: "Drinks"},
		{ID: "empty-parent", Name: "Nothing Here"},
		{ID: "empty-child", Name: "Also Nothing", ParentID: "empty-parent"},
	}
	buttons := []Button{
		{Label: "Cola", Code: "C1", ItemID: "i1", CategoryID: "drinks"},
	}

	groups := BuildCategoryGroups(buttons, cats)
	if len(groups) != 1 || groups[0].ID != "drinks" {
		t.Fatalf("expected only Drinks to survive pruning, got %+v", groups)
	}
}

// TestBuildCategoryGroups_UncategorizedBucket: buttons whose item has no
// category (or a category_id that no longer resolves) must not be dropped —
// they land in a trailing synthetic group, present only when non-empty.
func TestBuildCategoryGroups_UncategorizedBucket(t *testing.T) {
	cats := []data.CategoryNode{{ID: "drinks", Name: "Drinks"}}
	buttons := []Button{
		{Label: "Cola", Code: "C1", ItemID: "i1", CategoryID: "drinks"},
		{Label: "Loose Sweet", Code: "S1", ItemID: "i2", CategoryID: ""},
		{Label: "Stale Ref", Code: "S2", ItemID: "i3", CategoryID: "deleted-category"},
	}

	groups := BuildCategoryGroups(buttons, cats)
	if len(groups) != 2 {
		t.Fatalf("len(groups) = %d, want 2 (Drinks + uncategorized): %+v", len(groups), groups)
	}
	uncategorized := groups[len(groups)-1]
	if uncategorized.ID != "" {
		t.Fatalf("expected the uncategorized bucket last with empty ID, got %+v", uncategorized)
	}
	if len(uncategorized.Buttons) != 2 {
		t.Fatalf("expected both the categoryless and dangling-reference buttons bucketed, got %+v", uncategorized.Buttons)
	}

	// No uncategorized buttons at all -> no synthetic group appears.
	groups = BuildCategoryGroups(buttons[:1], cats)
	if len(groups) != 1 {
		t.Fatalf("expected no uncategorized group when nothing is uncategorized, got %+v", groups)
	}
}

// TestBuildCategoryGroups_SelfParentCycleDoesNotDropButtons: a category
// whose parent_id points at itself (malformed import/manual edit) must not
// swallow its buttons — before the cycle guard, such a category was never
// reachable from any root (it always found a "valid" parent — itself), so
// pruneEmptyCategoryGroup never visited it and its buttons vanished from
// the grid with no error, not even landing in the uncategorized bucket.
func TestBuildCategoryGroups_SelfParentCycleDoesNotDropButtons(t *testing.T) {
	cats := []data.CategoryNode{{ID: "a", Name: "A", ParentID: "a"}}
	buttons := []Button{{Label: "Latte", Code: "L1", ItemID: "i1", CategoryID: "a"}}

	groups := BuildCategoryGroups(buttons, cats)
	if len(groups) != 1 || groups[0].ID != "a" {
		t.Fatalf("expected the self-parented category to surface as a root, got %+v", groups)
	}
	if len(groups[0].Buttons) != 1 || groups[0].Buttons[0].Label != "Latte" {
		t.Fatalf("expected Latte to still render under category a, got %+v", groups[0].Buttons)
	}
}

// TestBuildCategoryGroups_TwoNodeCycleDoesNotDropButtons: same failure
// mode as the self-parent case, but via a two-category loop (a's parent is
// b, b's parent is a) — neither ever qualifies as a root under the old
// logic, so buttons in EITHER vanished silently.
func TestBuildCategoryGroups_TwoNodeCycleDoesNotDropButtons(t *testing.T) {
	cats := []data.CategoryNode{
		{ID: "a", Name: "A", ParentID: "b"},
		{ID: "b", Name: "B", ParentID: "a"},
	}
	buttons := []Button{
		{Label: "Latte", Code: "L1", ItemID: "i1", CategoryID: "a"},
		{Label: "Bun", Code: "B1", ItemID: "i2", CategoryID: "b"},
	}

	groups := BuildCategoryGroups(buttons, cats)
	total := 0
	var walk func([]*CategoryGroup)
	walk = func(gs []*CategoryGroup) {
		for _, g := range gs {
			total += len(g.Buttons)
			walk(g.Children)
		}
	}
	walk(groups)
	if total != 2 {
		t.Fatalf("expected both buttons to survive a 2-node parent_id cycle, got %d reachable: %+v", total, groups)
	}
}

// TestResolveCategoryColor_ExplicitOverridesAutoAndIsStable: a valid
// explicit hex color always wins; an absent/malformed one falls back to a
// deterministic per-ID color so the same category always renders the same
// swatch across page loads.
func TestResolveCategoryColor_ExplicitOverridesAutoAndIsStable(t *testing.T) {
	explicit := resolveCategoryColor(data.CategoryNode{ID: "x", Color: "#ABCDEF"})
	if explicit != "#ABCDEF" {
		t.Fatalf("expected explicit color to win, got %q", explicit)
	}

	malformed := resolveCategoryColor(data.CategoryNode{ID: "y", Color: "not-a-color"})
	if !hexColorPattern.MatchString(malformed) {
		t.Fatalf("expected a fallback hex color for malformed input, got %q", malformed)
	}

	auto1 := resolveCategoryColor(data.CategoryNode{ID: "same-id"})
	auto2 := resolveCategoryColor(data.CategoryNode{ID: "same-id"})
	if auto1 != auto2 {
		t.Fatalf("expected deterministic auto-color, got %q then %q", auto1, auto2)
	}
	if !hexColorPattern.MatchString(auto1) {
		t.Fatalf("expected auto-color to be a valid hex color, got %q", auto1)
	}
}
