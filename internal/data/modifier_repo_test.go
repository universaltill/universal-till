package data_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
)

func openModifierTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "mod.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// createLinkedGroup is CreateGroup (shop-wide, no item — ADR-0101) followed
// by LinkGroupToItem, the two-call shape every caller that has an item in
// hand now uses. Kept as a helper so the per-item fixtures below read as
// they always did. sortOrder is written to BOTH the group row (legacy, no
// read path consults it) and the link row (what the per-item reads use).
func createLinkedGroup(t *testing.T, repo *data.ModifierRepo, ctx context.Context, id, itemID, name string, required bool, minSelect, maxSelect, sortOrder int) (string, error) {
	t.Helper()
	gid, err := repo.CreateGroup(ctx, id, name, required, minSelect, maxSelect, sortOrder)
	if err != nil {
		return "", err
	}
	if err := repo.LinkGroupToItem(ctx, itemID, gid, sortOrder); err != nil {
		return "", err
	}
	return gid, nil
}

func TestModifierRepo_CreateAndListGroups(t *testing.T) {
	d := openModifierTestDB(t)
	ctx := context.Background()
	if _, err := d.DB.ExecContext(ctx, `INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm1','SKU1','Flat White',320,1)`); err != nil {
		t.Fatal(err)
	}

	repo := data.NewModifierRepo(d.DB)
	gid, err := createLinkedGroup(t, repo, ctx, "g1", "itm1", "Extras", false, 0, 2, 1)
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if _, err := repo.CreateOption(ctx, "o1", gid, "Extra shot", 50, 1); err != nil {
		t.Fatalf("CreateOption: %v", err)
	}
	if _, err := repo.CreateOption(ctx, "o2", gid, "Oat milk", 40, 2); err != nil {
		t.Fatalf("CreateOption: %v", err)
	}

	groups, err := repo.ListGroupsForItem(ctx, "itm1")
	if err != nil {
		t.Fatalf("ListGroupsForItem: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}
	g := groups[0]
	if g.Name != "Extras" || g.Required || g.MaxSelect != 2 {
		t.Fatalf("unexpected group: %+v", g)
	}
	if len(g.Options) != 2 || g.Options[0].Name != "Extra shot" || g.Options[0].PriceDeltaMinor != 50 {
		t.Fatalf("unexpected options: %+v", g.Options)
	}
}

// A deactivated group (or option) must not appear to the sale-time caller —
// this is how a manager retires a customization without deleting sale
// history that referenced it.
func TestModifierRepo_ListGroupsForItem_SkipsInactive(t *testing.T) {
	d := openModifierTestDB(t)
	ctx := context.Background()
	if _, err := d.DB.ExecContext(ctx, `INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm1','SKU1','Flat White',320,1)`); err != nil {
		t.Fatal(err)
	}
	repo := data.NewModifierRepo(d.DB)

	activeID, _ := createLinkedGroup(t, repo, ctx, "g-active", "itm1", "Extras", false, 0, 1, 1)
	inactiveID, _ := createLinkedGroup(t, repo, ctx, "g-inactive", "itm1", "Retired", false, 0, 1, 2)
	if err := repo.UpdateGroup(ctx, inactiveID, "Retired", false, 0, 1, 2, false); err != nil {
		t.Fatalf("UpdateGroup (deactivate): %v", err)
	}
	if _, err := repo.CreateOption(ctx, "o-active", activeID, "Extra shot", 50, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateOption(ctx, "o-inactive-opt", activeID, "Retired option", 10, 2); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateOption(ctx, "o-inactive-opt", "Retired option", 10, 2, false); err != nil {
		t.Fatalf("UpdateOption (deactivate): %v", err)
	}

	groups, err := repo.ListGroupsForItem(ctx, "itm1")
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].ID != activeID {
		t.Fatalf("expected only the active group, got %+v", groups)
	}
	if len(groups[0].Options) != 1 || groups[0].Options[0].ID != "o-active" {
		t.Fatalf("expected only the active option, got %+v", groups[0].Options)
	}
}

func TestModifierRepo_DeleteGroupCascadesOptions(t *testing.T) {
	d := openModifierTestDB(t)
	ctx := context.Background()
	if _, err := d.DB.ExecContext(ctx, `INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm1','SKU1','Flat White',320,1)`); err != nil {
		t.Fatal(err)
	}
	repo := data.NewModifierRepo(d.DB)
	gid, _ := createLinkedGroup(t, repo, ctx, "g1", "itm1", "Extras", false, 0, 1, 1)
	if _, err := repo.CreateOption(ctx, "o1", gid, "Extra shot", 50, 1); err != nil {
		t.Fatal(err)
	}

	if err := repo.DeleteGroup(ctx, gid); err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}

	var n int
	if err := d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM item_modifier_options WHERE group_id = ?`, gid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("expected options cascade-deleted with their group, got %d remaining", n)
	}
}

func TestModifierRepo_CreateOption_RejectsNegativeDelta(t *testing.T) {
	d := openModifierTestDB(t)
	ctx := context.Background()
	if _, err := d.DB.ExecContext(ctx, `INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm1','SKU1','Flat White',320,1)`); err != nil {
		t.Fatal(err)
	}
	repo := data.NewModifierRepo(d.DB)
	gid, _ := createLinkedGroup(t, repo, ctx, "g1", "itm1", "Extras", false, 0, 1, 1)

	if _, err := repo.CreateOption(ctx, "o1", gid, "No cheese", -50, 1); err == nil {
		t.Fatal("expected negative price_delta_minor to be rejected (additive-only in v1)")
	}
}

// ---------------------------------------------------------------------------
// ADR-0090 (ut-docs#2013): a modifier group is shareable across items via
// item_modifier_group_links. The group stays the identity/config record; the
// link table records which items use it, with a per-link sort_order.
// ---------------------------------------------------------------------------

// seedSharedGroupFixture creates items A ("Flat White") and B ("Latte"), a
// "Milk" group created on A with two options, and links it to B with a
// different per-item sort order (A: 5, B: 2).
func seedSharedGroupFixture(t *testing.T, ctx context.Context, d *db.DB, repo *data.ModifierRepo) {
	t.Helper()
	if _, err := d.DB.ExecContext(ctx, `INSERT INTO items (id, sku, name, base_price, is_active) VALUES
		('itm-a','SKU-A','Flat White',320,1), ('itm-b','SKU-B','Latte',350,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := createLinkedGroup(t, repo, ctx, "g-milk", "itm-a", "Milk", true, 1, 1, 5); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if _, err := repo.CreateOption(ctx, "o-oat", "g-milk", "Oat", 40, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateOption(ctx, "o-soy", "g-milk", "Soy", 30, 2); err != nil {
		t.Fatal(err)
	}
	if err := repo.LinkGroupToItem(ctx, "itm-b", "g-milk", 2); err != nil {
		t.Fatalf("LinkGroupToItem: %v", err)
	}
}

func TestModifierRepo_CreateGroup_AlsoWritesItsLinkRow(t *testing.T) {
	d := openModifierTestDB(t)
	ctx := context.Background()
	if _, err := d.DB.ExecContext(ctx, `INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm1','SKU1','Flat White',320,1)`); err != nil {
		t.Fatal(err)
	}
	repo := data.NewModifierRepo(d.DB)
	if _, err := createLinkedGroup(t, repo, ctx, "g1", "itm1", "Extras", false, 0, 2, 7); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	var item string
	var sort int
	if err := d.DB.QueryRowContext(ctx, `SELECT item_id, sort_order FROM item_modifier_group_links WHERE group_id = 'g1'`).Scan(&item, &sort); err != nil {
		t.Fatalf("CreateGroup must write the group's first link row too: %v", err)
	}
	if item != "itm1" || sort != 7 {
		t.Fatalf("link row = (%s, %d), want (itm1, 7)", item, sort)
	}
	// There is no anchor column any more (ADR-0101, migration 034): the
	// group row knows nothing about items.
	var n int
	if err := d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('item_modifier_groups') WHERE name = 'item_id'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("item_modifier_groups.item_id column count = %d err=%v, want 0", n, err)
	}
}

func TestModifierRepo_LinkGroupToItem_SecondItemSeesGroupWithLinkSortOrder(t *testing.T) {
	d := openModifierTestDB(t)
	ctx := context.Background()
	repo := data.NewModifierRepo(d.DB)
	seedSharedGroupFixture(t, ctx, d, repo)

	groups, err := repo.ListGroupsForItem(ctx, "itm-b")
	if err != nil {
		t.Fatalf("ListGroupsForItem(itm-b): %v", err)
	}
	if len(groups) != 1 || groups[0].ID != "g-milk" {
		t.Fatalf("itm-b must see the linked group, got %+v", groups)
	}
	g := groups[0]
	if g.SortOrder != 2 {
		t.Fatalf("SortOrder = %d, want 2 — must come from the link row, not the group's own column (5)", g.SortOrder)
	}
	if !g.Required || g.MinSelect != 1 || g.MaxSelect != 1 || g.Name != "Milk" {
		t.Fatalf("shared rule set must be the group's own: %+v", g)
	}
	if len(g.Options) != 2 || g.Options[0].Name != "Oat" || g.Options[1].Name != "Soy" {
		t.Fatalf("linked group must carry its full option list: %+v", g.Options)
	}

	// The original item keeps its own per-link sort order.
	groupsA, err := repo.ListGroupsForItem(ctx, "itm-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(groupsA) != 1 || groupsA[0].SortOrder != 5 || len(groupsA[0].Options) != 2 {
		t.Fatalf("itm-a's own view changed: %+v", groupsA)
	}

	// Re-linking upserts the sort order (ON CONFLICT DO UPDATE), never
	// duplicates the link.
	if err := repo.LinkGroupToItem(ctx, "itm-b", "g-milk", 9); err != nil {
		t.Fatalf("re-link: %v", err)
	}
	groups, _ = repo.ListGroupsForItem(ctx, "itm-b")
	if len(groups) != 1 || groups[0].SortOrder != 9 {
		t.Fatalf("re-link must update sort order in place, got %+v", groups)
	}

	if err := repo.LinkGroupToItem(ctx, "", "g-milk", 0); err == nil {
		t.Fatal("empty item id must be rejected")
	}
	if err := repo.LinkGroupToItem(ctx, "itm-b", "", 0); err == nil {
		t.Fatal("empty group id must be rejected")
	}
}

func TestModifierRepo_UnlinkGroupFromItem_RemovesOnlyThatLink(t *testing.T) {
	d := openModifierTestDB(t)
	ctx := context.Background()
	repo := data.NewModifierRepo(d.DB)
	seedSharedGroupFixture(t, ctx, d, repo)

	if err := repo.UnlinkGroupFromItem(ctx, "itm-a", "g-milk"); err != nil {
		t.Fatalf("UnlinkGroupFromItem: %v", err)
	}
	groupsA, err := repo.ListAllGroupsForItem(ctx, "itm-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(groupsA) != 0 {
		t.Fatalf("itm-a must no longer list the group, got %+v", groupsA)
	}
	groupsB, err := repo.ListGroupsForItem(ctx, "itm-b")
	if err != nil {
		t.Fatal(err)
	}
	if len(groupsB) != 1 || groupsB[0].ID != "g-milk" || len(groupsB[0].Options) != 2 {
		t.Fatalf("itm-b's link must be untouched, got %+v", groupsB)
	}
	var n int
	if err := d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM item_modifier_groups WHERE id = 'g-milk'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("group row must survive an unlink (n=%d err=%v)", n, err)
	}

	// Removing the LAST link is allowed and still never deletes the group
	// (ADR-0101 Decision 2): an unassigned group stays listed and editable
	// on /modifiers; DeleteGroup is the explicit full-delete action.
	if err := repo.UnlinkGroupFromItem(ctx, "itm-b", "g-milk"); err != nil {
		t.Fatal(err)
	}
	if err := d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM item_modifier_groups WHERE id = 'g-milk'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("group row must survive losing its last link (n=%d err=%v)", n, err)
	}
	if err := d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM item_modifier_options WHERE group_id = 'g-milk'`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("options must survive an unlink (n=%d err=%v)", n, err)
	}
	if err := repo.UnlinkGroupFromItem(ctx, "", "g-milk"); err == nil {
		t.Fatal("empty item id must be rejected")
	}
	if err := repo.UnlinkGroupFromItem(ctx, "itm-b", ""); err == nil {
		t.Fatal("empty group id must be rejected")
	}
}

// The button grid's "does tapping this open a picker?" flag must see a
// group an item only has via a link, and must still ignore an inactive one.
func TestModifierRepo_ItemIDsWithModifiers_SeesLinkedGroups(t *testing.T) {
	d := openModifierTestDB(t)
	ctx := context.Background()
	repo := data.NewModifierRepo(d.DB)
	seedSharedGroupFixture(t, ctx, d, repo)
	if _, err := d.DB.ExecContext(ctx, `INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm-c','SKU-C','Mocha',380,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := createLinkedGroup(t, repo, ctx, "g-retired", "itm-c", "Retired", false, 0, 1, 0); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateGroup(ctx, "g-retired", "Retired", false, 0, 1, 0, false); err != nil {
		t.Fatal(err)
	}
	got, err := repo.ItemIDsWithModifiers(ctx, []string{"itm-a", "itm-b", "itm-c"})
	if err != nil {
		t.Fatal(err)
	}
	if !got["itm-a"] || !got["itm-b"] {
		t.Fatalf("both linked items must be flagged: %#v", got)
	}
	if got["itm-c"] {
		t.Fatalf("an item whose only group is inactive must not be flagged: %#v", got)
	}
}

// The "attach an existing group" picker must offer a group linked elsewhere
// but not to THIS item, must never offer a group already linked to this
// item (nothing to attach twice), and must never offer an inactive group.
func TestModifierRepo_ListAttachableModifierGroups(t *testing.T) {
	d := openModifierTestDB(t)
	ctx := context.Background()
	repo := data.NewModifierRepo(d.DB)
	seedSharedGroupFixture(t, ctx, d, repo) // g-milk linked to itm-a and itm-b
	if _, err := d.DB.ExecContext(ctx, `INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm-c','SKU-C','Mocha',380,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := createLinkedGroup(t, repo, ctx, "g-retired", "itm-c", "Retired", false, 0, 1, 0); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateGroup(ctx, "g-retired", "Retired", false, 0, 1, 0, false); err != nil {
		t.Fatal(err)
	}

	got, err := repo.ListAttachableModifierGroups(ctx, "itm-c")
	if err != nil {
		t.Fatalf("ListAttachableModifierGroups: %v", err)
	}
	if len(got) != 1 || got[0].ID != "g-milk" || got[0].Name != "Milk" {
		t.Fatalf("expected only the active, not-yet-linked g-milk, got %+v", got)
	}
	if !got[0].IsActive {
		t.Fatalf("attachable groups must always report active: %+v", got[0])
	}

	// Already linked to itm-a — must not offer it again there.
	gotA, err := repo.ListAttachableModifierGroups(ctx, "itm-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(gotA) != 0 {
		t.Fatalf("itm-a already has g-milk, expected nothing attachable, got %+v", gotA)
	}

	if _, err := repo.ListAttachableModifierGroups(ctx, ""); err == nil {
		t.Fatal("empty item id must be rejected")
	}
}

// A prior detach can leave an item's own sort_orders sparse (e.g. 0 and 2
// after its middle group was removed) — NextGroupSortOrderForItem must
// still hand out a value that doesn't collide with an existing one
// (independent-review finding: a plain COUNT of the item's links would
// compute 2 here too, the same as the still-occupied slot).
func TestModifierRepo_NextGroupSortOrderForItem_HandlesSparseOrder(t *testing.T) {
	d := openModifierTestDB(t)
	ctx := context.Background()
	repo := data.NewModifierRepo(d.DB)
	if _, err := d.DB.ExecContext(ctx, `INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm1','SKU1','Flat White',320,1)`); err != nil {
		t.Fatal(err)
	}

	next, err := repo.NextGroupSortOrderForItem(ctx, "itm1")
	if err != nil {
		t.Fatalf("NextGroupSortOrderForItem (empty item): %v", err)
	}
	if next != 0 {
		t.Fatalf("expected 0 for an item with no groups yet, got %d", next)
	}

	if _, err := createLinkedGroup(t, repo, ctx, "g0", "itm1", "Zero", false, 0, 1, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := createLinkedGroup(t, repo, ctx, "g1", "itm1", "One", false, 0, 1, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := createLinkedGroup(t, repo, ctx, "g2", "itm1", "Two", false, 0, 1, 2); err != nil {
		t.Fatal(err)
	}
	// Remove the middle one — sort_orders 0 and 2 remain, 1 is now a gap.
	if err := repo.UnlinkGroupFromItem(ctx, "itm1", "g1"); err != nil {
		t.Fatal(err)
	}

	next, err = repo.NextGroupSortOrderForItem(ctx, "itm1")
	if err != nil {
		t.Fatal(err)
	}
	if next != 3 {
		t.Fatalf("expected MAX(sort_order)+1 = 3 (not len()-based 2, which would collide with g2's own sort_order), got %d", next)
	}

	if _, err := repo.NextGroupSortOrderForItem(ctx, ""); err == nil {
		t.Fatal("empty item id must be rejected")
	}
}

// ---------------------------------------------------------------------------
// ADR-0101 (ut-docs#2399): a modifier group is a shop-wide entity with no
// anchor item. Created bare, assigned by selection, deletable everywhere.
// ---------------------------------------------------------------------------

// A group can be created with no item at all, is listed by the shop-wide
// admin read with zero assignments, is offered by the pickers the item and
// category editors use, and is never resolved at checkout until assigned.
func TestModifierRepo_CreateGroup_NeedsNoItem(t *testing.T) {
	d := openModifierTestDB(t)
	ctx := context.Background()
	repo := data.NewModifierRepo(d.DB)
	if _, err := d.DB.ExecContext(ctx, `INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm1','SKU1','Flat White',320,1)`); err != nil {
		t.Fatal(err)
	}

	gid, err := repo.CreateGroup(ctx, "g-sauces", "Sauces", true, 1, 2, 0)
	if err != nil {
		t.Fatalf("CreateGroup without an item: %v", err)
	}
	if _, err := repo.CreateOption(ctx, "o-ketchup", gid, "Ketchup", 0, 1); err != nil {
		t.Fatal(err)
	}

	all, err := repo.ListAllModifierGroupsWithAssignments(ctx)
	if err != nil {
		t.Fatalf("ListAllModifierGroupsWithAssignments: %v", err)
	}
	if len(all) != 1 || all[0].ID != gid || all[0].Name != "Sauces" || !all[0].Required || all[0].MinSelect != 1 || all[0].MaxSelect != 2 {
		t.Fatalf("expected the standalone group once, got %+v", all)
	}
	if len(all[0].Categories) != 0 || len(all[0].Items) != 0 {
		t.Fatalf("a fresh group must have no assignments, got categories=%+v items=%+v", all[0].Categories, all[0].Items)
	}
	if len(all[0].Options) != 1 || all[0].Options[0].Name != "Ketchup" {
		t.Fatalf("options must load for an unassigned group, got %+v", all[0].Options)
	}

	active, err := repo.ListActiveModifierGroups(ctx)
	if err != nil || len(active) != 1 || active[0].ID != gid {
		t.Fatalf("the category editor's picker must offer an unassigned group: %+v err=%v", active, err)
	}
	attachable, err := repo.ListAttachableModifierGroups(ctx, "itm1")
	if err != nil || len(attachable) != 1 || attachable[0].ID != gid {
		t.Fatalf("the item editor's picker must offer an unassigned group: %+v err=%v", attachable, err)
	}
	resolved, err := repo.ResolveGroupsForItem(ctx, "itm1")
	if err != nil || len(resolved) != 0 {
		t.Fatalf("an unassigned group must never resolve at checkout, got %+v err=%v", resolved, err)
	}
	flagged, err := repo.ItemIDsWithModifiers(ctx, []string{"itm1"})
	if err != nil || flagged["itm1"] {
		t.Fatalf("an unassigned group must not flag any tile, got %#v err=%v", flagged, err)
	}

	if _, err := repo.CreateGroup(ctx, "", "No id", false, 0, 1, 0); err == nil {
		t.Fatal("empty id must be rejected")
	}
	if _, err := repo.CreateGroup(ctx, "g-blank", "", false, 0, 1, 0); err == nil {
		t.Fatal("empty name must be rejected")
	}
}

// The /modifiers read: each group exactly once regardless of how many
// items/categories use it, active or not, with every option (active or
// not), its category list and its item list (each in link order, items
// carrying their own active flag), ordered by group name then id.
func TestModifierRepo_ListAllModifierGroupsWithAssignments(t *testing.T) {
	d := openModifierTestDB(t)
	ctx := context.Background()
	repo := data.NewModifierRepo(d.DB)
	for _, q := range []string{
		`INSERT INTO categories (id, name) VALUES ('cat-drinks', 'Drinks'), ('cat-food', 'Food')`,
		`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm-a','A','Flat White',320,1), ('itm-b','B','Latte',350,1), ('itm-off','OFF','Retired Item',100,0)`,
	} {
		if _, err := d.DB.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	// Two groups named "Milk" with different ids, one "Extras" (inactive),
	// one bare "Zero".
	for _, g := range []struct {
		id, name string
		active   bool
	}{{"g-milk-2", "Milk", true}, {"g-milk-1", "Milk", true}, {"g-extras", "Extras", false}, {"g-zero", "Zero", true}} {
		if _, err := repo.CreateGroup(ctx, g.id, g.name, false, 0, 2, 0); err != nil {
			t.Fatal(err)
		}
		if !g.active {
			if err := repo.UpdateGroup(ctx, g.id, g.name, false, 0, 2, 0, false); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := repo.CreateOption(ctx, "o-oat", "g-milk-1", "Oat", 40, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateOption(ctx, "o-soy", "g-milk-1", "Soy", 30, 1); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateOption(ctx, "o-soy", "Soy", 30, 1, false); err != nil {
		t.Fatal(err)
	}
	// g-milk-1: three items (one deactivated) and two categories, with
	// link sort orders that disagree with name order.
	for _, l := range []struct {
		item string
		sort int
	}{{"itm-b", 0}, {"itm-a", 1}, {"itm-off", 2}} {
		if err := repo.LinkGroupToItem(ctx, l.item, "g-milk-1", l.sort); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.LinkGroupToCategory(ctx, "cat-food", "g-milk-1", 0); err != nil {
		t.Fatal(err)
	}
	if err := repo.LinkGroupToCategory(ctx, "cat-drinks", "g-milk-1", 1); err != nil {
		t.Fatal(err)
	}
	// g-extras (inactive): one category only.
	if err := repo.LinkGroupToCategory(ctx, "cat-drinks", "g-extras", 0); err != nil {
		t.Fatal(err)
	}

	got, err := repo.ListAllModifierGroupsWithAssignments(ctx)
	if err != nil {
		t.Fatalf("ListAllModifierGroupsWithAssignments: %v", err)
	}
	ids := make([]string, len(got))
	for i, g := range got {
		ids[i] = g.ID
	}
	want := []string{"g-extras", "g-milk-1", "g-milk-2", "g-zero"}
	if len(ids) != len(want) {
		t.Fatalf("ids = %v, want %v (each group exactly once, by name then id)", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("ids = %v, want %v (each group exactly once, by name then id)", ids, want)
		}
	}

	extras, milk := got[0], got[1]
	if extras.IsActive {
		t.Fatalf("inactive group must be listed AND reported inactive: %+v", extras)
	}
	if len(extras.Categories) != 1 || extras.Categories[0].ID != "cat-drinks" || extras.Categories[0].Name != "Drinks" || len(extras.Items) != 0 {
		t.Fatalf("g-extras assignments = cats %+v items %+v", extras.Categories, extras.Items)
	}
	if len(milk.Options) != 2 || milk.Options[0].Name != "Soy" || milk.Options[0].IsActive || milk.Options[1].Name != "Oat" {
		t.Fatalf("g-milk-1 options must be ALL options in sort order with active flags, got %+v", milk.Options)
	}
	if len(milk.Categories) != 2 || milk.Categories[0].Name != "Food" || milk.Categories[1].Name != "Drinks" {
		t.Fatalf("g-milk-1 categories must follow the category link's sort_order, got %+v", milk.Categories)
	}
	if len(milk.Items) != 3 || milk.Items[0].ID != "itm-b" || milk.Items[1].ID != "itm-a" || milk.Items[2].ID != "itm-off" {
		t.Fatalf("g-milk-1 items must follow the item link's sort_order, got %+v", milk.Items)
	}
	if milk.Items[0].Name != "Latte" || !milk.Items[0].IsActive || milk.Items[2].IsActive {
		t.Fatalf("g-milk-1 items must carry the item's name and active flag, got %+v", milk.Items)
	}
	if len(got[2].Categories) != 0 || len(got[2].Items) != 0 || len(got[3].Items) != 0 {
		t.Fatalf("unassigned groups must report empty lists: %+v %+v", got[2], got[3])
	}
}

func TestModifierRepo_ListAllModifierGroupsWithAssignments_EmptyShopReturnsNilNotError(t *testing.T) {
	d := openModifierTestDB(t)
	repo := data.NewModifierRepo(d.DB)
	got, err := repo.ListAllModifierGroupsWithAssignments(context.Background())
	if err != nil {
		t.Fatalf("ListAllModifierGroupsWithAssignments: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no groups in an empty shop, got %+v", got)
	}
}

// DeleteGroup is "remove everywhere": options, item links, category links
// and opt-outs all go; items and categories themselves are untouched.
func TestModifierRepo_DeleteGroup_CascadesEveryAssignment(t *testing.T) {
	d := openModifierTestDB(t)
	ctx := context.Background()
	repo := data.NewModifierRepo(d.DB)
	for _, q := range []string{
		`INSERT INTO categories (id, name) VALUES ('cat1', 'Drinks')`,
		`INSERT INTO items (id, sku, name, base_price, is_active, category_id) VALUES ('itm1','SKU1','Flat White',320,1,'cat1'), ('itm2','SKU2','Latte',350,1,'cat1')`,
	} {
		if _, err := d.DB.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if _, err := repo.CreateGroup(ctx, "g1", "Milk", false, 0, 1, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateOption(ctx, "o1", "g1", "Oat", 40, 1); err != nil {
		t.Fatal(err)
	}
	if err := repo.LinkGroupToItem(ctx, "itm1", "g1", 0); err != nil {
		t.Fatal(err)
	}
	if err := repo.LinkGroupToCategory(ctx, "cat1", "g1", 0); err != nil {
		t.Fatal(err)
	}
	if err := repo.OptOutItemFromGroup(ctx, "itm2", "g1"); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteGroup(ctx, "g1"); err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}
	for _, tc := range []struct {
		q    string
		want int
	}{
		{`SELECT COUNT(*) FROM item_modifier_groups`, 0},
		{`SELECT COUNT(*) FROM item_modifier_options`, 0},
		{`SELECT COUNT(*) FROM item_modifier_group_links`, 0},
		{`SELECT COUNT(*) FROM category_modifier_group_links`, 0},
		{`SELECT COUNT(*) FROM item_modifier_group_opt_outs`, 0},
		{`SELECT COUNT(*) FROM items`, 2},
		{`SELECT COUNT(*) FROM categories`, 1},
	} {
		var n int
		if err := d.DB.QueryRowContext(ctx, tc.q).Scan(&n); err != nil || n != tc.want {
			t.Fatalf("%s = %d err=%v, want %d", tc.q, n, err, tc.want)
		}
	}
	if err := repo.DeleteGroup(ctx, ""); err == nil {
		t.Fatal("empty id must be rejected")
	}
}

// Deleting an ITEM never touches a group row any more (ADR-0101 Decision
// 2): a group whose only item goes away simply becomes unassigned.
func TestModifierRepo_ItemDelete_LeavesGroupUnassigned(t *testing.T) {
	d := openModifierTestDB(t)
	ctx := context.Background()
	repo := data.NewModifierRepo(d.DB)
	if _, err := d.DB.ExecContext(ctx, `INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm1','SKU1','Flat White',320,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := createLinkedGroup(t, repo, ctx, "g1", "itm1", "Milk", false, 0, 1, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateOption(ctx, "o1", "g1", "Oat", 40, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.ExecContext(ctx, `DELETE FROM items WHERE id = 'itm1'`); err != nil {
		t.Fatal(err)
	}
	all, err := repo.ListAllModifierGroupsWithAssignments(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].ID != "g1" || len(all[0].Items) != 0 || len(all[0].Options) != 1 {
		t.Fatalf("the group must survive its only item's deletion, unassigned, with its options: %+v", all)
	}
}

// The category-side twin of NextGroupSortOrderForItem, same sparse-order
// contract (MAX+1, never a count).
func TestModifierRepo_NextGroupSortOrderForCategory_HandlesSparseOrder(t *testing.T) {
	d := openModifierTestDB(t)
	ctx := context.Background()
	repo := data.NewModifierRepo(d.DB)
	if _, err := d.DB.ExecContext(ctx, `INSERT INTO categories (id, name) VALUES ('cat1', 'Drinks')`); err != nil {
		t.Fatal(err)
	}
	next, err := repo.NextGroupSortOrderForCategory(ctx, "cat1")
	if err != nil || next != 0 {
		t.Fatalf("empty category: next=%d err=%v, want 0", next, err)
	}
	for i, id := range []string{"g0", "g1", "g2"} {
		if _, err := repo.CreateGroup(ctx, id, id, false, 0, 1, 0); err != nil {
			t.Fatal(err)
		}
		if err := repo.LinkGroupToCategory(ctx, "cat1", id, i); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.UnlinkGroupFromCategory(ctx, "cat1", "g1"); err != nil {
		t.Fatal(err)
	}
	next, err = repo.NextGroupSortOrderForCategory(ctx, "cat1")
	if err != nil || next != 3 {
		t.Fatalf("sparse category: next=%d err=%v, want MAX+1 = 3", next, err)
	}
	if _, err := repo.NextGroupSortOrderForCategory(ctx, ""); err == nil {
		t.Fatal("empty category id must be rejected")
	}
}
