package data_test

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
)

// ADR-0094 (ut-docs#1915): category-level modifier-group inheritance with a
// per-item opt-out. These tests pin the resolver contract the ADR's §3
// spells out —
//
//	resolved(item) = item's own directly-linked ACTIVE groups
//	               ∪ ( item.category's directly-linked ACTIVE groups
//	                   \ item's opt-out set )
//
// — plus the admin primitives #2284's editor will consume.

// seedCategoryFixture writes one category ('cat1'), one item in it ('itm1')
// and one item with NO category ('itm-nocat'). Groups are created against
// itm-anchor, a separate item that never appears in any assertion, so a
// group's ADR-0090 anchor link never leaks into what's being tested for
// itm1 — every group used against itm1 here is either category-linked or
// explicitly LinkGroupToItem'd by the test itself.
func seedCategoryFixture(t *testing.T, d *db.DB) {
	t.Helper()
	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO categories (id, name) VALUES ('cat1', 'Drinks')`,
		`INSERT INTO items (id, sku, name, base_price, is_active, category_id) VALUES ('itm1','SKU1','Flat White',320,1,'cat1')`,
		`INSERT INTO items (id, sku, name, base_price, is_active, category_id) VALUES ('itm-nocat','SKU2','Loose Tea',200,1,NULL)`,
		`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm-anchor','SKU9','Anchor',100,1)`,
	} {
		if _, err := d.DB.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

// createAnchoredGroup creates a group anchored (ADR-0090) to itm-anchor with
// one option, so an option-loading assertion has something to find.
func createAnchoredGroup(t *testing.T, repo *data.ModifierRepo, id, name string, sortOrder int) {
	t.Helper()
	ctx := context.Background()
	if _, err := repo.CreateGroup(ctx, id, "itm-anchor", name, false, 0, 1, sortOrder); err != nil {
		t.Fatalf("CreateGroup %s: %v", id, err)
	}
	if _, err := repo.CreateOption(ctx, "opt-"+id, id, "Option of "+name, 10, 1); err != nil {
		t.Fatalf("CreateOption for %s: %v", id, err)
	}
}

func groupIDs(groups []data.ModifierGroup) []string {
	ids := make([]string, len(groups))
	for i, g := range groups {
		ids[i] = g.ID
	}
	return ids
}

func assertGroupIDs(t *testing.T, groups []data.ModifierGroup, want ...string) {
	t.Helper()
	got := groupIDs(groups)
	if len(got) != len(want) {
		t.Fatalf("group ids = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("group ids = %v, want %v", got, want)
		}
	}
}

// 1. Basic inheritance: an item with no direct groups of its own offers its
// category's linked group at sale time.
func TestModifierRepo_ResolveGroupsForItem_InheritsCategoryGroup(t *testing.T) {
	d := openModifierTestDB(t)
	seedCategoryFixture(t, d)
	ctx := context.Background()
	repo := data.NewModifierRepo(d.DB)
	createAnchoredGroup(t, repo, "gA", "Milk", 0)
	if err := repo.LinkGroupToCategory(ctx, "cat1", "gA", 0); err != nil {
		t.Fatalf("LinkGroupToCategory: %v", err)
	}

	// ListGroupsForItem itself is unchanged (ADR-0094 Consequences): itm1
	// has no DIRECT link, so the narrower query still returns nothing.
	direct, err := repo.ListGroupsForItem(ctx, "itm1")
	if err != nil {
		t.Fatal(err)
	}
	if len(direct) != 0 {
		t.Fatalf("ListGroupsForItem must not see category links, got %+v", direct)
	}

	got, err := repo.ResolveGroupsForItem(ctx, "itm1")
	if err != nil {
		t.Fatalf("ResolveGroupsForItem: %v", err)
	}
	assertGroupIDs(t, got, "gA")
	if got[0].Name != "Milk" || !got[0].IsActive {
		t.Fatalf("unexpected inherited group: %+v", got[0])
	}
	if len(got[0].Options) != 1 || got[0].Options[0].ID != "opt-gA" {
		t.Fatalf("inherited group must carry its options, got %+v", got[0].Options)
	}
	if got[0].ItemID != "" {
		t.Fatalf("a category-inherited group is not item-scoped identity; ItemID = %q, want \"\"", got[0].ItemID)
	}
}

// 2. Union: the item's own direct group comes first, then the inherited one.
func TestModifierRepo_ResolveGroupsForItem_UnionOwnFirstThenInherited(t *testing.T) {
	d := openModifierTestDB(t)
	seedCategoryFixture(t, d)
	ctx := context.Background()
	repo := data.NewModifierRepo(d.DB)
	createAnchoredGroup(t, repo, "gA", "Milk", 0)
	createAnchoredGroup(t, repo, "gB", "Extras", 0)
	if err := repo.LinkGroupToCategory(ctx, "cat1", "gA", 0); err != nil {
		t.Fatal(err)
	}
	// Give the item's own link a HIGHER sort_order than the category link:
	// ordering is "own groups first, then inherited", never a merged sort.
	if err := repo.LinkGroupToItem(ctx, "itm1", "gB", 5); err != nil {
		t.Fatal(err)
	}

	got, err := repo.ResolveGroupsForItem(ctx, "itm1")
	if err != nil {
		t.Fatal(err)
	}
	assertGroupIDs(t, got, "gB", "gA")
	if got[0].ItemID != "itm1" || got[0].SortOrder != 5 {
		t.Fatalf("own group must keep its own link's ItemID/SortOrder, got %+v", got[0])
	}
	for _, g := range got {
		if len(g.Options) != 1 {
			t.Fatalf("group %s options = %+v, want exactly its one option", g.ID, g.Options)
		}
	}
}

// 3. Opt-out beats inheritance; opting back in restores it.
func TestModifierRepo_ResolveGroupsForItem_OptOutSuppressesAndOptInRestores(t *testing.T) {
	d := openModifierTestDB(t)
	seedCategoryFixture(t, d)
	ctx := context.Background()
	repo := data.NewModifierRepo(d.DB)
	createAnchoredGroup(t, repo, "gA", "Milk", 0)
	createAnchoredGroup(t, repo, "gC", "Syrup", 0)
	createAnchoredGroup(t, repo, "gB", "Extras", 0)
	if err := repo.LinkGroupToCategory(ctx, "cat1", "gA", 0); err != nil {
		t.Fatal(err)
	}
	if err := repo.LinkGroupToCategory(ctx, "cat1", "gC", 1); err != nil {
		t.Fatal(err)
	}
	if err := repo.LinkGroupToItem(ctx, "itm1", "gB", 0); err != nil {
		t.Fatal(err)
	}

	if err := repo.OptOutItemFromGroup(ctx, "itm1", "gA"); err != nil {
		t.Fatalf("OptOutItemFromGroup: %v", err)
	}
	// Opting out twice is a no-op, not a PK violation (INSERT OR IGNORE).
	if err := repo.OptOutItemFromGroup(ctx, "itm1", "gA"); err != nil {
		t.Fatalf("OptOutItemFromGroup (repeat): %v", err)
	}
	got, err := repo.ResolveGroupsForItem(ctx, "itm1")
	if err != nil {
		t.Fatal(err)
	}
	assertGroupIDs(t, got, "gB", "gC")

	if err := repo.OptInItemToGroup(ctx, "itm1", "gA"); err != nil {
		t.Fatalf("OptInItemToGroup: %v", err)
	}
	got, err = repo.ResolveGroupsForItem(ctx, "itm1")
	if err != nil {
		t.Fatal(err)
	}
	assertGroupIDs(t, got, "gB", "gA", "gC")
}

// An opt-out row only ever suppresses a CATEGORY-inherited group (ADR-0094
// §2) — it has no effect on a group the item is directly linked to.
func TestModifierRepo_ResolveGroupsForItem_OptOutDoesNotTouchDirectLink(t *testing.T) {
	d := openModifierTestDB(t)
	seedCategoryFixture(t, d)
	ctx := context.Background()
	repo := data.NewModifierRepo(d.DB)
	createAnchoredGroup(t, repo, "gB", "Extras", 0)
	if err := repo.LinkGroupToItem(ctx, "itm1", "gB", 0); err != nil {
		t.Fatal(err)
	}
	if err := repo.OptOutItemFromGroup(ctx, "itm1", "gB"); err != nil {
		t.Fatal(err)
	}
	got, err := repo.ResolveGroupsForItem(ctx, "itm1")
	if err != nil {
		t.Fatal(err)
	}
	assertGroupIDs(t, got, "gB")
}

// 4. No category: identical to ListGroupsForItem.
func TestModifierRepo_ResolveGroupsForItem_NoCategoryEqualsDirectOnly(t *testing.T) {
	d := openModifierTestDB(t)
	seedCategoryFixture(t, d)
	ctx := context.Background()
	repo := data.NewModifierRepo(d.DB)
	createAnchoredGroup(t, repo, "gA", "Milk", 0)
	createAnchoredGroup(t, repo, "gB", "Extras", 0)
	// A category link exists in the shop, but itm-nocat has no category.
	if err := repo.LinkGroupToCategory(ctx, "cat1", "gA", 0); err != nil {
		t.Fatal(err)
	}
	if err := repo.LinkGroupToItem(ctx, "itm-nocat", "gB", 0); err != nil {
		t.Fatal(err)
	}

	resolved, err := repo.ResolveGroupsForItem(ctx, "itm-nocat")
	if err != nil {
		t.Fatal(err)
	}
	direct, err := repo.ListGroupsForItem(ctx, "itm-nocat")
	if err != nil {
		t.Fatal(err)
	}
	assertGroupIDs(t, resolved, "gB")
	assertGroupIDs(t, direct, "gB")
	if resolved[0].ItemID != direct[0].ItemID || resolved[0].SortOrder != direct[0].SortOrder || len(resolved[0].Options) != len(direct[0].Options) {
		t.Fatalf("ResolveGroupsForItem for an item without a category must equal ListGroupsForItem: %+v vs %+v", resolved[0], direct[0])
	}

	// And an item that doesn't exist at all resolves to nothing, no error.
	none, err := repo.ResolveGroupsForItem(ctx, "does-not-exist")
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Fatalf("unknown item resolved to %+v, want nothing", none)
	}
}

// 5. Dedup: a group linked both directly and via the category appears once,
// as the item's own copy (its own link's sort_order — ADR-0094 §3).
func TestModifierRepo_ResolveGroupsForItem_DedupKeepsOwnCopy(t *testing.T) {
	d := openModifierTestDB(t)
	seedCategoryFixture(t, d)
	ctx := context.Background()
	repo := data.NewModifierRepo(d.DB)
	createAnchoredGroup(t, repo, "gA", "Milk", 0)
	createAnchoredGroup(t, repo, "gC", "Syrup", 0)
	if err := repo.LinkGroupToCategory(ctx, "cat1", "gA", 0); err != nil {
		t.Fatal(err)
	}
	if err := repo.LinkGroupToCategory(ctx, "cat1", "gC", 1); err != nil {
		t.Fatal(err)
	}
	if err := repo.LinkGroupToItem(ctx, "itm1", "gA", 7); err != nil {
		t.Fatal(err)
	}

	got, err := repo.ResolveGroupsForItem(ctx, "itm1")
	if err != nil {
		t.Fatal(err)
	}
	assertGroupIDs(t, got, "gA", "gC")
	if got[0].ItemID != "itm1" || got[0].SortOrder != 7 {
		t.Fatalf("deduped group must be the item's OWN copy (ItemID itm1, SortOrder 7), got %+v", got[0])
	}
	if len(got[0].Options) != 1 || len(got[1].Options) != 1 {
		t.Fatalf("options must be loaded exactly once per surviving group: %+v", got)
	}
}

// 6. A deactivated category-linked group is never offered at sale time, and
// ListGroupsForCategory (active variant) hides it too while
// ListAllGroupsForCategory still shows it for the admin editor.
func TestModifierRepo_CategoryGroups_InactiveExcluded(t *testing.T) {
	d := openModifierTestDB(t)
	seedCategoryFixture(t, d)
	ctx := context.Background()
	repo := data.NewModifierRepo(d.DB)
	createAnchoredGroup(t, repo, "gA", "Milk", 0)
	createAnchoredGroup(t, repo, "gRetired", "Retired", 0)
	if err := repo.UpdateGroup(ctx, "gRetired", "Retired", false, 0, 1, 0, false); err != nil {
		t.Fatal(err)
	}
	if err := repo.LinkGroupToCategory(ctx, "cat1", "gRetired", 0); err != nil {
		t.Fatal(err)
	}
	if err := repo.LinkGroupToCategory(ctx, "cat1", "gA", 1); err != nil {
		t.Fatal(err)
	}

	resolved, err := repo.ResolveGroupsForItem(ctx, "itm1")
	if err != nil {
		t.Fatal(err)
	}
	assertGroupIDs(t, resolved, "gA")

	active, err := repo.ListGroupsForCategory(ctx, "cat1")
	if err != nil {
		t.Fatal(err)
	}
	assertGroupIDs(t, active, "gA")

	all, err := repo.ListAllGroupsForCategory(ctx, "cat1")
	if err != nil {
		t.Fatal(err)
	}
	assertGroupIDs(t, all, "gRetired", "gA")
	if all[0].IsActive || !all[1].IsActive {
		t.Fatalf("ListAllGroupsForCategory must report IsActive faithfully: %+v", all)
	}

	inherited, err := repo.ListInheritedGroupsForItem(ctx, "itm1")
	if err != nil {
		t.Fatal(err)
	}
	assertGroupIDs(t, inherited, "gA")
}

// 7. ListInheritedGroupsForItem annotates each of the category's active
// groups with the item's own opt-out state, and returns nil for an item
// with no category.
func TestModifierRepo_ListInheritedGroupsForItem_OptedOutFlags(t *testing.T) {
	d := openModifierTestDB(t)
	seedCategoryFixture(t, d)
	ctx := context.Background()
	repo := data.NewModifierRepo(d.DB)
	createAnchoredGroup(t, repo, "gA", "Milk", 0)
	createAnchoredGroup(t, repo, "gC", "Syrup", 0)
	if err := repo.LinkGroupToCategory(ctx, "cat1", "gA", 0); err != nil {
		t.Fatal(err)
	}
	if err := repo.LinkGroupToCategory(ctx, "cat1", "gC", 1); err != nil {
		t.Fatal(err)
	}
	if err := repo.OptOutItemFromGroup(ctx, "itm1", "gC"); err != nil {
		t.Fatal(err)
	}

	got, err := repo.ListInheritedGroupsForItem(ctx, "itm1")
	if err != nil {
		t.Fatalf("ListInheritedGroupsForItem: %v", err)
	}
	assertGroupIDs(t, got, "gA", "gC")
	if got[0].OptedOut {
		t.Fatalf("gA is not opted out, got %+v", got[0])
	}
	if !got[1].OptedOut {
		t.Fatalf("gC must be flagged OptedOut, got %+v", got[1])
	}
	for _, g := range got {
		if len(g.Options) != 1 {
			t.Fatalf("inherited listing must load options: %+v", g)
		}
		if g.ItemID != "" {
			t.Fatalf("inherited listing is not item-scoped identity; ItemID = %q", g.ItemID)
		}
	}
	// OptedOut is populated ONLY by ListInheritedGroupsForItem — the sale-
	// time resolver drops an opted-out group entirely rather than flagging
	// it, and the category listings never see an item at all.
	cat, err := repo.ListGroupsForCategory(ctx, "cat1")
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range cat {
		if g.OptedOut {
			t.Fatalf("ListGroupsForCategory must leave OptedOut false: %+v", g)
		}
	}

	nocat, err := repo.ListInheritedGroupsForItem(ctx, "itm-nocat")
	if err != nil {
		t.Fatal(err)
	}
	if nocat != nil {
		t.Fatalf("item without a category must inherit nil, got %+v", nocat)
	}
}

// 8. Category link CRUD round-trip: link, list (ordered by the link's own
// sort_order), re-link updates sort_order in place, unlink, list again.
func TestModifierRepo_CategoryLinks_RoundTrip(t *testing.T) {
	d := openModifierTestDB(t)
	seedCategoryFixture(t, d)
	ctx := context.Background()
	repo := data.NewModifierRepo(d.DB)
	createAnchoredGroup(t, repo, "gA", "Milk", 0)
	createAnchoredGroup(t, repo, "gC", "Syrup", 0)

	empty, err := repo.ListAllGroupsForCategory(ctx, "cat1")
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 {
		t.Fatalf("fresh category must have no groups, got %+v", empty)
	}

	if err := repo.LinkGroupToCategory(ctx, "cat1", "gA", 2); err != nil {
		t.Fatal(err)
	}
	if err := repo.LinkGroupToCategory(ctx, "cat1", "gC", 1); err != nil {
		t.Fatal(err)
	}
	got, err := repo.ListGroupsForCategory(ctx, "cat1")
	if err != nil {
		t.Fatal(err)
	}
	assertGroupIDs(t, got, "gC", "gA")
	if got[0].SortOrder != 1 || got[1].SortOrder != 2 {
		t.Fatalf("SortOrder must come from the category link row: %+v", got)
	}
	if got[0].ItemID != "" {
		t.Fatalf("category listing must leave ItemID empty, got %q", got[0].ItemID)
	}

	// Re-link with a new sort_order: ON CONFLICT DO UPDATE, not a PK error.
	if err := repo.LinkGroupToCategory(ctx, "cat1", "gA", 0); err != nil {
		t.Fatalf("re-link (sort_order update): %v", err)
	}
	got, err = repo.ListGroupsForCategory(ctx, "cat1")
	if err != nil {
		t.Fatal(err)
	}
	assertGroupIDs(t, got, "gA", "gC")

	if err := repo.UnlinkGroupFromCategory(ctx, "cat1", "gA"); err != nil {
		t.Fatalf("UnlinkGroupFromCategory: %v", err)
	}
	got, err = repo.ListGroupsForCategory(ctx, "cat1")
	if err != nil {
		t.Fatal(err)
	}
	assertGroupIDs(t, got, "gC")

	// Unlinking a category never touches the group row or its item links
	// (ADR-0094 Decision 1: a category is never a group's sole owner).
	if n, err := repo.GroupLinkCount(ctx, "gA"); err != nil || n != 1 {
		t.Fatalf("gA item-link count after category unlink = %d, %v; want 1", n, err)
	}

	// Input validation mirrors the item-link primitives.
	if err := repo.LinkGroupToCategory(ctx, "", "gA", 0); err == nil {
		t.Fatal("LinkGroupToCategory with empty category_id must error")
	}
	if err := repo.LinkGroupToCategory(ctx, "cat1", "", 0); err == nil {
		t.Fatal("LinkGroupToCategory with empty group_id must error")
	}
	if err := repo.UnlinkGroupFromCategory(ctx, "", "gA"); err == nil {
		t.Fatal("UnlinkGroupFromCategory with empty category_id must error")
	}
	if err := repo.OptOutItemFromGroup(ctx, "", "gA"); err == nil {
		t.Fatal("OptOutItemFromGroup with empty item_id must error")
	}
	if err := repo.OptInItemToGroup(ctx, "itm1", ""); err == nil {
		t.Fatal("OptInItemToGroup with empty group_id must error")
	}
}

// Hard-deleting a category cascades only its attachment rows away — the
// group and its item links survive (ADR-0094 Decision 1, "no anchor
// problem here").
func TestModifierRepo_CategoryDelete_CascadesLinksOnly(t *testing.T) {
	d := openModifierTestDB(t)
	seedCategoryFixture(t, d)
	ctx := context.Background()
	repo := data.NewModifierRepo(d.DB)
	createAnchoredGroup(t, repo, "gA", "Milk", 0)
	if err := repo.LinkGroupToCategory(ctx, "cat1", "gA", 0); err != nil {
		t.Fatal(err)
	}
	if err := repo.OptOutItemFromGroup(ctx, "itm1", "gA"); err != nil {
		t.Fatal(err)
	}
	// Detach itm1 from the category first (items.category_id has no ON
	// DELETE CASCADE of its own), then hard-delete the category.
	if _, err := d.DB.ExecContext(ctx, `UPDATE items SET category_id = NULL WHERE id = 'itm1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.ExecContext(ctx, `DELETE FROM categories WHERE id = 'cat1'`); err != nil {
		t.Fatalf("delete category: %v", err)
	}
	var links int
	if err := d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM category_modifier_group_links WHERE category_id = 'cat1'`).Scan(&links); err != nil {
		t.Fatal(err)
	}
	if links != 0 {
		t.Fatalf("category links must cascade away with the category, got %d", links)
	}
	if n, err := repo.GroupLinkCount(ctx, "gA"); err != nil || n != 1 {
		t.Fatalf("group must survive its category's deletion: item-link count = %d, %v", n, err)
	}
	// The opt-out row is keyed by (item, group), not by category, so it is
	// deliberately left alone here — it becomes relevant again the moment
	// the item joins a category that links gA.
	var optOuts int
	if err := d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM item_modifier_group_opt_outs WHERE item_id = 'itm1' AND group_id = 'gA'`).Scan(&optOuts); err != nil {
		t.Fatal(err)
	}
	if optOuts != 1 {
		t.Fatalf("opt-out row count = %d, want 1 (keyed by item+group, not category)", optOuts)
	}
	// ...and a deleted GROUP takes its opt-out rows with it.
	if err := repo.DeleteGroup(ctx, "gA"); err != nil {
		t.Fatal(err)
	}
	if err := d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM item_modifier_group_opt_outs WHERE group_id = 'gA'`).Scan(&optOuts); err != nil {
		t.Fatal(err)
	}
	if optOuts != 0 {
		t.Fatalf("opt-out rows must cascade with their group, got %d", optOuts)
	}
}

// ItemIDsWithModifiers is the tile-tap gate (buttons.html/self_order_grid.html
// via HasModifiers) deciding whether tapping an item opens the customization
// picker at all — it MUST agree with ResolveGroupsForItem's own resolution,
// or an item that only inherits a group from its category silently adds
// straight to the basket with the inherited group never offered
// (independent-review finding, ut-docs#1915: shipped once checking only
// item_modifier_group_links).
func TestModifierRepo_ItemIDsWithModifiers_AgreesWithResolveGroupsForItem(t *testing.T) {
	d := openModifierTestDB(t)
	seedCategoryFixture(t, d)
	ctx := context.Background()
	repo := data.NewModifierRepo(d.DB)
	createAnchoredGroup(t, repo, "gA", "Milk", 0)
	if err := repo.LinkGroupToCategory(ctx, "cat1", "gA", 0); err != nil {
		t.Fatal(err)
	}

	// itm1 has NO direct link — only category inheritance. The old
	// (item_modifier_group_links-only) query would report false here.
	got, err := repo.ItemIDsWithModifiers(ctx, []string{"itm1", "itm-nocat"})
	if err != nil {
		t.Fatalf("ItemIDsWithModifiers: %v", err)
	}
	if !got["itm1"] {
		t.Fatalf("itm1 inherits an active group from its category, must report true: %+v", got)
	}
	if got["itm-nocat"] {
		t.Fatalf("itm-nocat has no category and no direct links, must be absent: %+v", got)
	}

	// Sanity: this must track ResolveGroupsForItem's own verdict, not just
	// this test's own expectation.
	resolved, err := repo.ResolveGroupsForItem(ctx, "itm1")
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved) == 0 {
		t.Fatal("test setup broken: itm1 should resolve at least one group")
	}

	// Opting out of the only inherited group must flip the gate back to
	// false — a tile for an item with nothing left to offer must not open
	// an empty picker.
	if err := repo.OptOutItemFromGroup(ctx, "itm1", "gA"); err != nil {
		t.Fatal(err)
	}
	got, err = repo.ItemIDsWithModifiers(ctx, []string{"itm1"})
	if err != nil {
		t.Fatal(err)
	}
	if got["itm1"] {
		t.Fatalf("itm1 opted out of its only inherited group, must report false: %+v", got)
	}
}

// A group that is directly linked to an item AND linked to the item's
// category AND opted out: the opt-out only ever suppresses the CATEGORY
// attachment (ADR-0094 §2) — the direct link keeps the group live, and the
// item keeps seeing it exactly once (its own copy), not zero times.
func TestModifierRepo_ResolveGroupsForItem_DirectLinkSurvivesOptOutOfSameGroup(t *testing.T) {
	d := openModifierTestDB(t)
	seedCategoryFixture(t, d)
	ctx := context.Background()
	repo := data.NewModifierRepo(d.DB)
	createAnchoredGroup(t, repo, "gA", "Milk", 0)
	if err := repo.LinkGroupToCategory(ctx, "cat1", "gA", 0); err != nil {
		t.Fatal(err)
	}
	if err := repo.LinkGroupToItem(ctx, "itm1", "gA", 3); err != nil {
		t.Fatal(err)
	}
	if err := repo.OptOutItemFromGroup(ctx, "itm1", "gA"); err != nil {
		t.Fatal(err)
	}

	got, err := repo.ResolveGroupsForItem(ctx, "itm1")
	if err != nil {
		t.Fatal(err)
	}
	assertGroupIDs(t, got, "gA")
	if got[0].ItemID != "itm1" || got[0].SortOrder != 3 {
		t.Fatalf("opted-out-but-directly-linked group must still be the item's OWN copy, got %+v", got[0])
	}

	gate, err := repo.ItemIDsWithModifiers(ctx, []string{"itm1"})
	if err != nil {
		t.Fatal(err)
	}
	if !gate["itm1"] {
		t.Fatalf("direct link must keep the tile-tap gate true despite the opt-out row: %+v", gate)
	}
}
