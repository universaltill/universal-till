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
