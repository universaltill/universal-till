package data_test

import (
	"context"
	"path/filepath"
	"sync"
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

func TestModifierRepo_CreateAndListGroups(t *testing.T) {
	d := openModifierTestDB(t)
	ctx := context.Background()
	if _, err := d.DB.ExecContext(ctx, `INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm1','SKU1','Flat White',320,1)`); err != nil {
		t.Fatal(err)
	}

	repo := data.NewModifierRepo(d.DB)
	gid, err := repo.CreateGroup(ctx, "g1", "itm1", "Extras", false, 0, 2, 1)
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

	activeID, _ := repo.CreateGroup(ctx, "g-active", "itm1", "Extras", false, 0, 1, 1)
	inactiveID, _ := repo.CreateGroup(ctx, "g-inactive", "itm1", "Retired", false, 0, 1, 2)
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
	gid, _ := repo.CreateGroup(ctx, "g1", "itm1", "Extras", false, 0, 1, 1)
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
	gid, _ := repo.CreateGroup(ctx, "g1", "itm1", "Extras", false, 0, 1, 1)

	if _, err := repo.CreateOption(ctx, "o1", gid, "No cheese", -50, 1); err == nil {
		t.Fatal("expected negative price_delta_minor to be rejected (additive-only in v1)")
	}
}

// ListShopModifierGroups (ut-docs#1899) is the shop-wide browse query behind
// the new /modifiers screen — today a modifier group can only be seen by
// opening the one item it belongs to; this is what lets a merchant see every
// group across the whole catalog in one place.
func TestModifierRepo_ListShopModifierGroups_ReturnsEveryItemsActiveGroups(t *testing.T) {
	d := openModifierTestDB(t)
	ctx := context.Background()
	if _, err := d.DB.ExecContext(ctx, `INSERT INTO items (id, sku, name, base_price, is_active) VALUES
		('itm1','SKU1','Flat White',320,1), ('itm2','SKU2','Latte',350,1)`); err != nil {
		t.Fatal(err)
	}
	repo := data.NewModifierRepo(d.DB)

	gid1, err := repo.CreateGroup(ctx, "g1", "itm1", "Extras", true, 1, 2, 1)
	if err != nil {
		t.Fatalf("CreateGroup itm1: %v", err)
	}
	if _, err := repo.CreateOption(ctx, "o1", gid1, "Extra shot", 50, 1); err != nil {
		t.Fatal(err)
	}
	gid2, err := repo.CreateGroup(ctx, "g2", "itm2", "Milk", false, 0, 1, 1)
	if err != nil {
		t.Fatalf("CreateGroup itm2: %v", err)
	}
	if _, err := repo.CreateOption(ctx, "o2", gid2, "Oat milk", 40, 1); err != nil {
		t.Fatal(err)
	}

	groups, err := repo.ListShopModifierGroups(ctx)
	if err != nil {
		t.Fatalf("ListShopModifierGroups: %v", err)
	}
	if len(groups) != 2 {
		t.Fatalf("expected 2 groups across the shop, got %d: %+v", len(groups), groups)
	}
	// Ordered by item name (Flat White before Latte).
	if groups[0].ItemName != "Flat White" || groups[0].Name != "Extras" {
		t.Fatalf("unexpected first group: %+v", groups[0])
	}
	if !groups[0].Required || groups[0].MinSelect != 1 {
		t.Fatalf("expected group[0] required flags to survive the join: %+v", groups[0])
	}
	if len(groups[0].Options) != 1 || groups[0].Options[0].Name != "Extra shot" {
		t.Fatalf("expected group[0] to carry its option: %+v", groups[0].Options)
	}
	if groups[1].ItemName != "Latte" || groups[1].Name != "Milk" {
		t.Fatalf("unexpected second group: %+v", groups[1])
	}
}

// A deactivated group must not appear on the shop-wide browse screen either
// — same visibility contract as the sale-time ListGroupsForItem.
func TestModifierRepo_ListShopModifierGroups_SkipsInactiveGroupsAndOptions(t *testing.T) {
	d := openModifierTestDB(t)
	ctx := context.Background()
	if _, err := d.DB.ExecContext(ctx, `INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm1','SKU1','Flat White',320,1)`); err != nil {
		t.Fatal(err)
	}
	repo := data.NewModifierRepo(d.DB)

	gidActive, err := repo.CreateGroup(ctx, "g1", "itm1", "Extras", false, 0, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateOption(ctx, "o1", gidActive, "Extra shot", 50, 1); err != nil {
		t.Fatal(err)
	}
	inactiveOptID, err := repo.CreateOption(ctx, "o2", gidActive, "Discontinued syrup", 30, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateOption(ctx, inactiveOptID, "Discontinued syrup", 30, 2, false); err != nil {
		t.Fatal(err)
	}

	gidInactive, err := repo.CreateGroup(ctx, "g2", "itm1", "Retired", false, 0, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateGroup(ctx, gidInactive, "Retired", false, 0, 1, 2, false); err != nil {
		t.Fatal(err)
	}

	groups, err := repo.ListShopModifierGroups(ctx)
	if err != nil {
		t.Fatalf("ListShopModifierGroups: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("expected only the active group, got %d: %+v", len(groups), groups)
	}
	if groups[0].Name != "Extras" {
		t.Fatalf("unexpected group: %+v", groups[0])
	}
	if len(groups[0].Options) != 1 || groups[0].Options[0].Name != "Extra shot" {
		t.Fatalf("expected only the active option, got: %+v", groups[0].Options)
	}
}

// A deactivated ITEM's groups must not appear either — independent review,
// ut-docs#1899. DeactivateItem never touches item_modifier_groups.is_active,
// and ListItems already filters deactivated items off /catalog (the only
// place a group can be edited or deactivated), so without this check a
// deactivated item's groups would render on /modifiers forever, labelled
// with a name the merchant can no longer find or act on.
func TestModifierRepo_ListShopModifierGroups_SkipsGroupsOfDeactivatedItem(t *testing.T) {
	d := openModifierTestDB(t)
	ctx := context.Background()
	if _, err := d.DB.ExecContext(ctx, `INSERT INTO items (id, sku, name, base_price, is_active) VALUES
		('itm1','SKU1','Flat White',320,1), ('itm2','SKU2','Latte',350,1)`); err != nil {
		t.Fatal(err)
	}
	repo := data.NewModifierRepo(d.DB)

	gid1, err := repo.CreateGroup(ctx, "g1", "itm1", "Extras", false, 0, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateOption(ctx, "o1", gid1, "Extra shot", 50, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateGroup(ctx, "g2", "itm2", "Milk", false, 0, 1, 1); err != nil {
		t.Fatal(err)
	}

	// Deactivate itm1 the same way DeactivateItem does — it never touches
	// item_modifier_groups.is_active, which is exactly the gap this test
	// guards.
	if _, err := d.DB.ExecContext(ctx, `UPDATE items SET is_active = 0 WHERE id = 'itm1'`); err != nil {
		t.Fatal(err)
	}

	groups, err := repo.ListShopModifierGroups(ctx)
	if err != nil {
		t.Fatalf("ListShopModifierGroups: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("expected only itm2's group (itm1 is deactivated), got %d: %+v", len(groups), groups)
	}
	if groups[0].ItemName != "Latte" || groups[0].Name != "Milk" {
		t.Fatalf("unexpected group: %+v", groups[0])
	}
}

func TestModifierRepo_ListShopModifierGroups_EmptyShopReturnsNilNotError(t *testing.T) {
	d := openModifierTestDB(t)
	repo := data.NewModifierRepo(d.DB)

	groups, err := repo.ListShopModifierGroups(context.Background())
	if err != nil {
		t.Fatalf("ListShopModifierGroups: %v", err)
	}
	if len(groups) != 0 {
		t.Fatalf("expected no groups in an empty shop, got %+v", groups)
	}
}

// ListAllShopModifierGroups (ut-docs#1957) is the admin-equivalent of
// ListShopModifierGroups: /modifiers became the full CRUD home for modifier
// groups, and an admin managing them shop-wide must still see a deactivated
// group/option so it can be reactivated — same reason ListAllGroupsForItem
// exists beside ListGroupsForItem for the per-item case. The read-only
// browse query (ListShopModifierGroups) keeps hiding inactive rows; this is
// a separate method, not a behavior change to that one.
func TestModifierRepo_ListAllShopModifierGroups_IncludesInactiveGroupsAndOptions(t *testing.T) {
	d := openModifierTestDB(t)
	ctx := context.Background()
	if _, err := d.DB.ExecContext(ctx, `INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm1','SKU1','Flat White',320,1)`); err != nil {
		t.Fatal(err)
	}
	repo := data.NewModifierRepo(d.DB)

	gidActive, err := repo.CreateGroup(ctx, "g1", "itm1", "Extras", false, 0, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateOption(ctx, "o1", gidActive, "Extra shot", 50, 1); err != nil {
		t.Fatal(err)
	}
	inactiveOptID, err := repo.CreateOption(ctx, "o2", gidActive, "Discontinued syrup", 30, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateOption(ctx, inactiveOptID, "Discontinued syrup", 30, 2, false); err != nil {
		t.Fatal(err)
	}

	gidInactive, err := repo.CreateGroup(ctx, "g2", "itm1", "Retired", false, 0, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateGroup(ctx, gidInactive, "Retired", false, 0, 1, 2, false); err != nil {
		t.Fatal(err)
	}

	groups, err := repo.ListAllShopModifierGroups(ctx)
	if err != nil {
		t.Fatalf("ListAllShopModifierGroups: %v", err)
	}
	if len(groups) != 2 {
		t.Fatalf("expected both the active AND the deactivated group, got %d: %+v", len(groups), groups)
	}
	var extras, retired *data.ModifierGroup
	for i := range groups {
		switch groups[i].Name {
		case "Extras":
			extras = &groups[i]
		case "Retired":
			retired = &groups[i]
		}
	}
	if extras == nil || retired == nil {
		t.Fatalf("expected both Extras and Retired groups, got: %+v", groups)
	}
	if retired.IsActive {
		t.Fatalf("expected Retired group to be reported inactive: %+v", retired)
	}
	if len(extras.Options) != 2 {
		t.Fatalf("expected Extras to carry BOTH its active and inactive option, got: %+v", extras.Options)
	}
	foundInactiveOpt := false
	for _, o := range extras.Options {
		if o.Name == "Discontinued syrup" && !o.IsActive {
			foundInactiveOpt = true
		}
	}
	if !foundInactiveOpt {
		t.Fatalf("expected the deactivated option to be present and marked inactive: %+v", extras.Options)
	}
}

// A deactivated item's groups must still be excluded even from the admin
// variant — same reasoning as ListShopModifierGroups's own equivalent test:
// there is nowhere left to reach/manage that item's groups once the item
// itself is gone from /catalog.
func TestModifierRepo_ListAllShopModifierGroups_SkipsGroupsOfDeactivatedItem(t *testing.T) {
	d := openModifierTestDB(t)
	ctx := context.Background()
	if _, err := d.DB.ExecContext(ctx, `INSERT INTO items (id, sku, name, base_price, is_active) VALUES
		('itm1','SKU1','Flat White',320,1), ('itm2','SKU2','Latte',350,1)`); err != nil {
		t.Fatal(err)
	}
	repo := data.NewModifierRepo(d.DB)

	if _, err := repo.CreateGroup(ctx, "g1", "itm1", "Extras", false, 0, 1, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateGroup(ctx, "g2", "itm2", "Milk", false, 0, 1, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.ExecContext(ctx, `UPDATE items SET is_active = 0 WHERE id = 'itm1'`); err != nil {
		t.Fatal(err)
	}

	groups, err := repo.ListAllShopModifierGroups(ctx)
	if err != nil {
		t.Fatalf("ListAllShopModifierGroups: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("expected only itm2's group (itm1 is deactivated), got %d: %+v", len(groups), groups)
	}
	if groups[0].ItemName != "Latte" || groups[0].Name != "Milk" {
		t.Fatalf("unexpected group: %+v", groups[0])
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
	if _, err := repo.CreateGroup(ctx, "g-milk", "itm-a", "Milk", true, 1, 1, 5); err != nil {
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
	if _, err := repo.CreateGroup(ctx, "g1", "itm1", "Extras", false, 0, 2, 7); err != nil {
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
	// The legacy anchor column is still populated (NOT NULL, ADR-0090 §2).
	if err := d.DB.QueryRowContext(ctx, `SELECT item_id FROM item_modifier_groups WHERE id = 'g1'`).Scan(&item); err != nil || item != "itm1" {
		t.Fatalf("anchor item_id = %q err=%v, want itm1", item, err)
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
	if g.ItemID != "itm-b" {
		t.Fatalf("ItemID = %q, want itm-b (the item this listing is for, not the legacy anchor)", g.ItemID)
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

// The single most important regression test in ADR-0090's data layer: once
// a group can appear in MULTIPLE shop-wide rows (one per linked item), an
// options-attachment index keyed by group id alone (the pre-#2013 byID map)
// keeps only the LAST row per group and attaches that group's options to
// that one row only — every earlier item showing the same group would render
// with an empty option list. Both shop-wide variants must show the full
// option list under EVERY item the group is linked to.
func TestModifierRepo_ListShopModifierGroups_SharedGroupCarriesFullOptionsUnderEveryItem(t *testing.T) {
	d := openModifierTestDB(t)
	ctx := context.Background()
	repo := data.NewModifierRepo(d.DB)
	seedSharedGroupFixture(t, ctx, d, repo)
	// A third item sharing the same group, plus an unshared group of its own
	// so the shared and unshared rows interleave in the ordered result.
	if _, err := d.DB.ExecContext(ctx, `INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm-c','SKU-C','Mocha',380,1)`); err != nil {
		t.Fatal(err)
	}
	if err := repo.LinkGroupToItem(ctx, "itm-c", "g-milk", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateGroup(ctx, "g-extras", "itm-c", "Extras", false, 0, 2, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateOption(ctx, "o-shot", "g-extras", "Extra shot", 50, 1); err != nil {
		t.Fatal(err)
	}

	for name, list := range map[string]func(context.Context) ([]data.ModifierGroup, error){
		"ListShopModifierGroups":    repo.ListShopModifierGroups,
		"ListAllShopModifierGroups": repo.ListAllShopModifierGroups,
	} {
		t.Run(name, func(t *testing.T) {
			groups, err := list(ctx)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			// Flat White/Milk, Latte/Milk, Mocha/Extras, Mocha/Milk.
			if len(groups) != 4 {
				t.Fatalf("want 4 rows (Milk under 3 items + Extras once), got %d: %+v", len(groups), groups)
			}
			seenMilkUnder := map[string]int{}
			for _, g := range groups {
				if g.ID != "g-milk" {
					continue
				}
				seenMilkUnder[g.ItemID]++
				if g.ItemName == "" {
					t.Errorf("Milk row under %s has no ItemName", g.ItemID)
				}
				if len(g.Options) != 2 {
					t.Errorf("Milk under %s (%s) carries %d options, want 2 — options attached to only one of the shared group's rows: %+v", g.ItemID, g.ItemName, len(g.Options), g.Options)
				}
			}
			for _, item := range []string{"itm-a", "itm-b", "itm-c"} {
				if seenMilkUnder[item] != 1 {
					t.Errorf("Milk must appear exactly once under %s, seen %d times", item, seenMilkUnder[item])
				}
			}
			// Ordering: item name, then per-link sort_order — Mocha lists
			// Extras (0) before Milk (1); the Milk row's SortOrder is the
			// LINK's, not the group's own column (5).
			if groups[0].ItemName != "Flat White" || groups[0].SortOrder != 5 ||
				groups[1].ItemName != "Latte" || groups[1].SortOrder != 2 ||
				groups[2].ItemName != "Mocha" || groups[2].Name != "Extras" ||
				groups[3].ItemName != "Mocha" || groups[3].Name != "Milk" || groups[3].SortOrder != 1 {
				t.Fatalf("unexpected order/sort_order: %+v", groups)
			}
		})
	}
}

// A shared group whose ONE link points at a deactivated item must still be
// listed under its other, active item — the i.is_active filter is per link
// row, not per group.
func TestModifierRepo_ListShopModifierGroups_SharedGroupHidesOnlyDeactivatedItemsRow(t *testing.T) {
	d := openModifierTestDB(t)
	ctx := context.Background()
	repo := data.NewModifierRepo(d.DB)
	seedSharedGroupFixture(t, ctx, d, repo)
	if _, err := d.DB.ExecContext(ctx, `UPDATE items SET is_active = 0 WHERE id = 'itm-a'`); err != nil {
		t.Fatal(err)
	}
	groups, err := repo.ListShopModifierGroups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].ItemID != "itm-b" || len(groups[0].Options) != 2 {
		t.Fatalf("want Milk under Latte only, with both options, got %+v", groups)
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

	// Removing the LAST link still never deletes the group: an orphaned
	// group stays manageable via /modifiers; DeleteGroup is the explicit
	// full-delete action.
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
	if _, err := repo.CreateGroup(ctx, "g-retired", "itm-c", "Retired", false, 0, 1, 0); err != nil {
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
	if _, err := repo.CreateGroup(ctx, "g-retired", "itm-c", "Retired", false, 0, 1, 0); err != nil {
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

	if _, err := repo.CreateGroup(ctx, "g0", "itm1", "Zero", false, 0, 1, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateGroup(ctx, "g1", "itm1", "One", false, 0, 1, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateGroup(ctx, "g2", "itm1", "Two", false, 0, 1, 2); err != nil {
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

func TestModifierRepo_GroupLinkCount(t *testing.T) {
	d := openModifierTestDB(t)
	ctx := context.Background()
	repo := data.NewModifierRepo(d.DB)
	seedSharedGroupFixture(t, ctx, d, repo) // g-milk linked to itm-a and itm-b

	n, err := repo.GroupLinkCount(ctx, "g-milk")
	if err != nil {
		t.Fatalf("GroupLinkCount: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 links, got %d", n)
	}

	if err := repo.UnlinkGroupFromItem(ctx, "itm-a", "g-milk"); err != nil {
		t.Fatal(err)
	}
	n, err = repo.GroupLinkCount(ctx, "g-milk")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 link after unlinking one, got %d", n)
	}

	if _, err := repo.GroupLinkCount(ctx, ""); err == nil {
		t.Fatal("empty group id must be rejected")
	}
	if n, err := repo.GroupLinkCount(ctx, "nonexistent"); err != nil || n != 0 {
		t.Fatalf("unknown group id should count 0, got n=%d err=%v", n, err)
	}
}

// UnlinkGroupFromItemUnlessLastLink is the atomic guard behind the detach
// handler (ut-docs#2046, independent-review finding): a naive
// count-then-delete has a TOCTOU window between the two statements. This
// pins the single-statement replacement's own correctness directly: it
// unlinks when >1 link exists, refuses (returns false, changes nothing) at
// exactly 1, and the "wins the race" case is exercised for real by firing
// two unlinks concurrently against a group with exactly 2 links and
// asserting exactly one succeeds and one link always survives.
func TestModifierRepo_UnlinkGroupFromItemUnlessLastLink(t *testing.T) {
	d := openModifierTestDB(t)
	ctx := context.Background()
	repo := data.NewModifierRepo(d.DB)
	seedSharedGroupFixture(t, ctx, d, repo) // g-milk linked to itm-a and itm-b

	ok, err := repo.UnlinkGroupFromItemUnlessLastLink(ctx, "itm-a", "g-milk")
	if err != nil {
		t.Fatalf("UnlinkGroupFromItemUnlessLastLink: %v", err)
	}
	if !ok {
		t.Fatal("expected the unlink to succeed with 2 links present")
	}
	var n int
	if err := d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM item_modifier_group_links WHERE group_id = 'g-milk'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("expected exactly 1 remaining link, got n=%d err=%v", n, err)
	}

	// Now only itm-b's link remains — refuse to remove it.
	ok, err = repo.UnlinkGroupFromItemUnlessLastLink(ctx, "itm-b", "g-milk")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected the last remaining link to be refused")
	}
	if err := d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM item_modifier_group_links WHERE group_id = 'g-milk'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("the last link must survive a refused unlink, got n=%d err=%v", n, err)
	}

	if _, err := repo.UnlinkGroupFromItemUnlessLastLink(ctx, "", "g-milk"); err == nil {
		t.Fatal("empty item id must be rejected")
	}
	if _, err := repo.UnlinkGroupFromItemUnlessLastLink(ctx, "itm-b", ""); err == nil {
		t.Fatal("empty group id must be rejected")
	}
}

// The real TOCTOU scenario: two goroutines racing to unlink a 2-link
// group's own two different items concurrently. A naive count-then-delete
// could let both observe count==2, both pass their check, and both delete —
// orphaning the group. The atomic conditional DELETE must let exactly one
// through.
func TestModifierRepo_UnlinkGroupFromItemUnlessLastLink_ConcurrentRaceLeavesOneLink(t *testing.T) {
	d := openModifierTestDB(t)
	ctx := context.Background()
	repo := data.NewModifierRepo(d.DB)
	seedSharedGroupFixture(t, ctx, d, repo) // g-milk linked to itm-a and itm-b

	var wg sync.WaitGroup
	results := make([]bool, 2)
	errs := make([]error, 2)
	items := []string{"itm-a", "itm-b"}
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = repo.UnlinkGroupFromItemUnlessLastLink(ctx, items[i], "g-milk")
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}
	succeeded := 0
	for _, ok := range results {
		if ok {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("expected exactly 1 of the 2 concurrent unlinks to succeed, got %d (results=%v)", succeeded, results)
	}
	var n int
	if err := d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM item_modifier_group_links WHERE group_id = 'g-milk'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("the group must never end up with 0 links: found %d", n)
	}
}
