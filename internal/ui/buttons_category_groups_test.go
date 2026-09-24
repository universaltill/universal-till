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

	groups := BuildCategoryGroups(buttons, cats, nil)
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

// TestStampLocked_RecursesIntoNestedCategoriesAndUncategorized (ut-docs#2361
// review): stampLocked's own tree walk was only ever exercised, in this
// repo, against seedOneButton's flat two-uncategorized-button fixture
// (internal/pages/buttons_api_catalog_management_gate_test.go) — its
// recursion into g.Children (a nested category, mirroring
// TestBuildCategoryGroups_NestsByParentID's own fixture above) had no test
// of its own, so a future BuildCategoryGroups change reshaping that
// recursion could silently stop locking a nested-category button with
// nothing here to catch it.
func TestStampLocked_RecursesIntoNestedCategoriesAndUncategorized(t *testing.T) {
	cats := []data.CategoryNode{
		{ID: "drinks", Name: "Drinks", SortOrder: 0},
		{ID: "hot-drinks", Name: "Hot Drinks", ParentID: "drinks", SortOrder: 0},
	}
	buttons := []Button{
		{Label: "Latte", Code: "L1", ItemID: "i1", CategoryID: "hot-drinks"},
		{Label: "Loose", Code: "L2", ItemID: "i2"}, // no CategoryID -> uncategorized bucket
	}

	for _, granted := range []bool{true, false} {
		groups := BuildCategoryGroups(buttons, cats, nil)
		stampLocked(groups, granted)

		nested := groups[0].Children[0].Buttons[0]
		if nested.Locked != !granted {
			t.Fatalf("granted=%v: nested-category button Locked=%v, want %v", granted, nested.Locked, !granted)
		}
		uncategorized := groups[1].Buttons[0]
		if uncategorized.Locked != !granted {
			t.Fatalf("granted=%v: uncategorized button Locked=%v, want %v", granted, uncategorized.Locked, !granted)
		}
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

	groups := BuildCategoryGroups(buttons, cats, nil)
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

	groups := BuildCategoryGroups(buttons, cats, nil)
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
	groups = BuildCategoryGroups(buttons[:1], cats, nil)
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

	groups := BuildCategoryGroups(buttons, cats, nil)
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

	groups := BuildCategoryGroups(buttons, cats, nil)
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

// TestBuildCategoryGroups_AncestorNameLabelsDescendantsNotRoots pins
// ut-docs#2198's disambiguation data: a root category itself carries no
// AncestorName (nothing to disambiguate a top-level bucket against), but
// every descendant — direct child and grandchild alike — is labeled with
// its OWN top-level root's name, not its immediate parent's, so two
// same-named subcategories under different top-level categories can be
// told apart by which root each actually traces back to.
func TestBuildCategoryGroups_AncestorNameLabelsDescendantsNotRoots(t *testing.T) {
	cats := []data.CategoryNode{
		{ID: "food", Name: "Food"},
		{ID: "household", Name: "Household"},
		{ID: "food-specials", Name: "Specials", ParentID: "food"},
		{ID: "household-specials", Name: "Specials", ParentID: "household"},
		{ID: "food-specials-sub", Name: "Deep", ParentID: "food-specials"},
	}
	buttons := []Button{
		{Label: "A", Code: "A1", ItemID: "i1", CategoryID: "food-specials"},
		{Label: "B", Code: "B1", ItemID: "i2", CategoryID: "household-specials"},
		{Label: "C", Code: "C1", ItemID: "i3", CategoryID: "food-specials-sub"},
	}

	groups := BuildCategoryGroups(buttons, cats, nil)
	byID := map[string]*CategoryGroup{}
	var walk func([]*CategoryGroup)
	walk = func(gs []*CategoryGroup) {
		for _, g := range gs {
			byID[g.ID] = g
			walk(g.Children)
		}
	}
	walk(groups)

	if byID["food"] == nil || byID["food"].AncestorName != "" {
		t.Fatalf("expected root Food to carry no AncestorName, got %+v", byID["food"])
	}
	if byID["household"] == nil || byID["household"].AncestorName != "" {
		t.Fatalf("expected root Household to carry no AncestorName, got %+v", byID["household"])
	}
	if got := byID["food-specials"].AncestorName; got != "Food" {
		t.Fatalf("expected Food's Specials child to carry AncestorName %q, got %q", "Food", got)
	}
	if got := byID["household-specials"].AncestorName; got != "Household" {
		t.Fatalf("expected Household's Specials child to carry AncestorName %q, got %q", "Household", got)
	}
	if got := byID["food-specials-sub"].AncestorName; got != "Food" {
		t.Fatalf("expected a grandchild to still carry its top-level root's name (Food), got %q", got)
	}
}

// TestBuildCategoryGroups_ItemCountAloneSurvivesPruning (ut-docs#2498): a
// category with zero quick buttons anywhere in its subtree but a non-zero
// itemCounts entry must survive pruning — the exact bug this card fixes
// (previously only a quick button, never an active-item count, could save a
// branch from being pruned).
func TestBuildCategoryGroups_ItemCountAloneSurvivesPruning(t *testing.T) {
	cats := []data.CategoryNode{
		{ID: "drinks", Name: "Drinks"},
		{ID: "snacks", Name: "Snacks"},
	}
	var buttons []Button // no quick buttons anywhere
	itemCounts := map[string]int{"drinks": 3, "snacks": 0}

	groups := BuildCategoryGroups(buttons, cats, itemCounts)
	if len(groups) != 1 || groups[0].ID != "drinks" {
		t.Fatalf("expected only Drinks (has active items) to survive pruning, got %+v", groups)
	}
	if groups[0].HasButtons {
		t.Fatalf("expected HasButtons=false: Drinks survived via item count alone with zero quick buttons, got %+v", groups[0])
	}
}

// TestBuildCategoryGroups_HasButtonsPropagatesFromDescendant: a parent with
// no OWN buttons but a child that does have one must still report
// HasButtons=true — the "kept child keeps its parent" OR that already
// governs plain pruning survival must extend to this field too.
func TestBuildCategoryGroups_HasButtonsPropagatesFromDescendant(t *testing.T) {
	cats := []data.CategoryNode{
		{ID: "food", Name: "Food"},
		{ID: "food-specials", Name: "Specials", ParentID: "food"},
	}
	buttons := []Button{{Label: "Pie", Code: "P1", ItemID: "i1", CategoryID: "food-specials"}}

	groups := BuildCategoryGroups(buttons, cats, nil)
	if len(groups) != 1 || groups[0].ID != "food" {
		t.Fatalf("expected Food to survive (via its child's button), got %+v", groups)
	}
	if !groups[0].HasButtons {
		t.Fatalf("expected Food.HasButtons=true (propagated from its Specials child), got %+v", groups[0])
	}
	if len(groups[0].Children) != 1 || !groups[0].Children[0].HasButtons {
		t.Fatalf("expected Specials.HasButtons=true (has its own button), got %+v", groups[0].Children)
	}
}

// TestBuildCategoryGroups_HasButtonsFalseWhenSurvivingViaItemCountOnly
// mirrors the propagation test above for the all-item-count, zero-buttons
// case, two levels deep — HasButtons must read false all the way up, so the
// template shows the empty state at every level that needs it.
func TestBuildCategoryGroups_HasButtonsFalseWhenSurvivingViaItemCountOnly(t *testing.T) {
	cats := []data.CategoryNode{
		{ID: "food", Name: "Food"},
		{ID: "food-specials", Name: "Specials", ParentID: "food"},
	}
	itemCounts := map[string]int{"food-specials": 1}

	groups := BuildCategoryGroups(nil, cats, itemCounts)
	if len(groups) != 1 || groups[0].ID != "food" {
		t.Fatalf("expected Food to survive (via its child's item count), got %+v", groups)
	}
	if groups[0].HasButtons {
		t.Fatalf("expected Food.HasButtons=false, got %+v", groups[0])
	}
	if len(groups[0].Children) != 1 || groups[0].Children[0].HasButtons {
		t.Fatalf("expected Specials.HasButtons=false too, got %+v", groups[0].Children)
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

// TestBuildCategoryGroups_PosIsGlobalSortIndex (ut-docs#2339): every tile
// the sale screen renders carries its index in the GLOBAL sort_order list
// (ButtonVM.Pos), not its index within its category group. The grid groups
// tiles by category, so the DOM order is not the global order once
// categories interleave (A(cat1) B(cat2) C(cat1) renders as [A C] [B]);
// the jiggle-mode reorder (app.js's utTileJiggle) uses Pos to rebuild the
// full global list it POSTs to /api/buttons/reorder, re-filling only the
// slots the reordered group already occupied, so a drag within one category
// never disturbs where other categories' buttons sit — the same
// "nearest same-category neighbour" semantics the ut-docs#2285 sheet's
// server-side Move had, now computed client-side from these indices.
func TestBuildCategoryGroups_PosIsGlobalSortIndex(t *testing.T) {
	cats := []data.CategoryNode{
		{ID: "cat1", Name: "One"},
		{ID: "cat2", Name: "Two"},
	}
	buttons := []Button{
		{Label: "A", Code: "A", ItemID: "iA", CategoryID: "cat1"},
		{Label: "B", Code: "B", ItemID: "iB", CategoryID: "cat2"},
		{Label: "C", Code: "C", ItemID: "iC", CategoryID: "cat1"},
		{Label: "U", Code: "U", ItemID: "iU"}, // uncategorized bucket
	}
	groups := BuildCategoryGroups(buttons, cats, nil)
	got := map[string]int{}
	var walk func(g *CategoryGroup)
	walk = func(g *CategoryGroup) {
		for _, b := range g.Buttons {
			got[b.Code] = b.Pos
		}
		for _, c := range g.Children {
			walk(c)
		}
	}
	for _, g := range groups {
		walk(g)
	}
	want := map[string]int{"A": 0, "B": 1, "C": 2, "U": 3}
	for code, pos := range want {
		if got[code] != pos {
			t.Fatalf("Pos[%s] = %d, want %d (global sort index, not the in-group index); all: %v", code, got[code], pos, got)
		}
	}
}
