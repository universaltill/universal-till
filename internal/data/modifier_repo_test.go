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
