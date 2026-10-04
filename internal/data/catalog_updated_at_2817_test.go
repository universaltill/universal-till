package data_test

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
)

// ut-docs#2817: the catalogue write-through's conflict check compares the
// updated_at an additional till's editor loaded with the main till's row.
// That only works if every catalogue write advances updated_at, and if the
// four tables that had no such column (categories, item_variants,
// shortcut_buttons, item_modifier_groups) now carry one (migration 065).

const staleStamp = "2000-01-01 00:00:00"

func newUpdatedAtFixture(t *testing.T) *db.DB {
	t.Helper()
	d := openModifierTestDB(t)
	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO categories (id, name) VALUES ('cat1', 'Drinks'), ('cat2', 'Food')`,
		`INSERT INTO items (id, sku, name, base_price, is_active, category_id) VALUES ('itm1','SKU1','Flat White',320,1,'cat1')`,
		`INSERT INTO item_variants (id, item_id, sku, name, price, is_active) VALUES ('var1','itm1','SKU1-L','Large',380,1)`,
		`INSERT INTO item_modifier_groups (id, name) VALUES ('grp1','Milk')`,
		`INSERT INTO shortcut_buttons (barcode, label, item_id, sort_order) VALUES ('SKU1','Flat White','itm1',0)`,
	} {
		if _, err := d.DB.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	return d
}

// stale backdates one row so the test can see a write move updated_at.
func stale(t *testing.T, d *db.DB, table, pkCol, id string) {
	t.Helper()
	if _, err := d.DB.Exec(`UPDATE `+table+` SET updated_at = ? WHERE `+pkCol+` = ?`, staleStamp, id); err != nil {
		t.Fatalf("backdate %s %s: %v", table, id, err)
	}
}

func updatedAt(t *testing.T, repo *data.CatalogRepo, kind, id string) string {
	t.Helper()
	v, ok, err := repo.CatalogUpdatedAt(context.Background(), kind, id)
	if err != nil || !ok {
		t.Fatalf("CatalogUpdatedAt(%s, %s) = %q, %v, %v", kind, id, v, ok, err)
	}
	return v
}

func TestCatalogUpdatedAt_NewRowsCarryAStamp(t *testing.T) {
	d := newUpdatedAtFixture(t)
	repo := data.NewCatalogRepo(d.DB)
	for _, c := range []struct{ kind, id string }{
		{data.CatalogKindItem, "itm1"},
		{data.CatalogKindVariant, "var1"},
		{data.CatalogKindCategory, "cat1"},
		{data.CatalogKindButton, "SKU1"},
		{data.CatalogKindModifierGroup, "grp1"},
	} {
		if got := updatedAt(t, repo, c.kind, c.id); got == "" {
			t.Errorf("%s %s: a freshly inserted row has an empty updated_at", c.kind, c.id)
		}
	}
	if _, ok, err := repo.CatalogUpdatedAt(context.Background(), data.CatalogKindItem, "missing"); err != nil || ok {
		t.Errorf("missing row: ok=%v err=%v, want not found", ok, err)
	}
	if _, _, err := repo.CatalogUpdatedAt(context.Background(), "tables", "x"); err == nil {
		t.Error("an unknown kind must be an error, never a query on an arbitrary table")
	}
}

func TestCatalogUpdatedAt_EveryWriteAdvancesIt(t *testing.T) {
	ctx := context.Background()
	d := newUpdatedAtFixture(t)
	repo := data.NewCatalogRepo(d.DB)
	mods := data.NewModifierRepo(d.DB)
	buttons := data.NewShortcutsRepo(d.DB)
	cat1 := "cat1"

	cases := []struct {
		name, kind, table, pk, id string
		write                     func() error
	}{
		{"item editor save", data.CatalogKindItem, "items", "id", "itm1", func() error {
			_, err := repo.UpdateItemReturningWasActive(ctx, catalogtypes.ItemInput{ID: "itm1", Name: "Renamed", BasePrice: 350, Unit: "pcs", CategoryID: &cat1, IsActive: true})
			return err
		}},
		{"variant save", data.CatalogKindVariant, "item_variants", "id", "var1", func() error {
			return repo.UpdateVariant(ctx, catalogtypes.VariantInput{ID: "var1", ItemID: "itm1", Name: "Larger", Price: 390, IsActive: true})
		}},
		{"variant deactivate", data.CatalogKindVariant, "item_variants", "id", "var1", func() error { return repo.DeactivateVariant(ctx, "var1") }},
		{"category save", data.CatalogKindCategory, "categories", "id", "cat2", func() error {
			return repo.UpdateCategoryWithHidden(ctx, "cat2", "Hot food", "", nil)
		}},
		{"category active", data.CatalogKindCategory, "categories", "id", "cat2", func() error { return repo.SetCategoryActive(ctx, "cat2", false) }},
		{"category reorder", data.CatalogKindCategory, "categories", "id", "cat1", func() error { return repo.SetCategorySortOrder(ctx, []string{"cat2", "cat1"}) }},
		{"modifier group save", data.CatalogKindModifierGroup, "item_modifier_groups", "id", "grp1", func() error {
			return mods.UpdateGroup(ctx, "grp1", "Milks", false, 0, 1, 0, true)
		}},
		{"button reorder", data.CatalogKindButton, "shortcut_buttons", "barcode", "SKU1", func() error { return buttons.UpdateOrder(ctx, []string{"SKU1"}) }},
		{"button relabel", data.CatalogKindButton, "shortcut_buttons", "barcode", "SKU1", func() error {
			return buttons.AddButton(ctx, data.ShortcutButton{Barcode: "SKU1", Label: "Flat White XL", ItemID: "itm1"})
		}},
		// Last: a deactivated item can no longer take a quick button.
		{"item deactivate", data.CatalogKindItem, "items", "id", "itm1", func() error { return repo.DeactivateItem(ctx, "itm1") }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stale(t, d, c.table, c.pk, c.id)
			if err := c.write(); err != nil {
				t.Fatalf("write: %v", err)
			}
			if got := updatedAt(t, repo, c.kind, c.id); got == staleStamp {
				t.Fatalf("%s left %s.updated_at at %q — the conflict check could not see this change", c.name, c.table, got)
			}
		})
	}
}
