package data_test

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#3606: the reads behind every conflict-checked catalogue form carry
// the record's updated_at, so the form can send the stamp it rendered.

func TestCatalogUpdatedAt_ItemButtonKindIsTheRowAddButtonWrites(t *testing.T) {
	ctx := context.Background()
	d := newUpdatedAtFixture(t)
	repo := data.NewCatalogRepo(d.DB)
	if _, err := d.DB.Exec(`INSERT INTO shortcut_buttons (barcode, label, item_id, sort_order, updated_at) VALUES ('ZZZ', 'Later', 'itm1', 5, '2001-01-01 00:00:00')`); err != nil {
		t.Fatal(err)
	}
	stale(t, d, "shortcut_buttons", "barcode", "SKU1")
	if got := updatedAt(t, repo, data.CatalogKindItemButton, "itm1"); got != staleStamp {
		t.Fatalf("item_button itm1 = %q, want SKU1's stamp (the first by sort_order, barcode)", got)
	}
	// Adding under a NEW code re-labels SKU1 (AddButton's existing-row
	// branch): the kind sees that write.
	if err := data.NewShortcutsRepo(d.DB).AddButton(ctx, data.ShortcutButton{Barcode: "NEWCODE", Label: "Flat White XL", ItemID: "itm1"}); err != nil {
		t.Fatal(err)
	}
	if got := updatedAt(t, repo, data.CatalogKindItemButton, "itm1"); got == staleStamp {
		t.Fatalf("item_button itm1 still %q after AddButton re-labelled the row", got)
	}
	if _, ok, err := repo.CatalogUpdatedAt(ctx, data.CatalogKindButton, "NEWCODE"); err != nil || ok {
		t.Fatalf("a NEWCODE row exists (%v, %v) — AddButton should have re-labelled SKU1", ok, err)
	}
	// An item with no button: nothing to conflict on (a plain add).
	if _, ok, err := repo.CatalogUpdatedAt(ctx, data.CatalogKindItemButton, "missing"); err != nil || ok {
		t.Fatalf("item_button missing = %v, %v", ok, err)
	}
}

func TestCatalogListReads_CarryUpdatedAt(t *testing.T) {
	ctx := context.Background()
	d := newUpdatedAtFixture(t)
	for _, q := range []string{
		`UPDATE categories SET updated_at = '2026-03-03 01:00:00' WHERE id = 'cat1'`,
		`UPDATE item_variants SET updated_at = '2026-03-03 02:00:00' WHERE id = 'var1'`,
		`UPDATE item_modifier_groups SET updated_at = '2026-03-03 03:00:00' WHERE id = 'grp1'`,
		`UPDATE items SET updated_at = '2026-03-03 04:00:00' WHERE id = 'itm1'`,
	} {
		if _, err := d.DB.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	repo := data.NewCatalogRepo(d.DB)
	cats, err := repo.ListCategoriesForAdmin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range cats {
		if c.ID == "cat1" {
			found = true
			if c.UpdatedAt != "2026-03-03 01:00:00" {
				t.Errorf("ListCategoriesForAdmin cat1.UpdatedAt = %q", c.UpdatedAt)
			}
		}
	}
	if !found {
		t.Fatal("cat1 not listed")
	}
	vars, err := repo.VariantsForItem(ctx, "itm1")
	if err != nil || len(vars) != 1 || vars[0].UpdatedAt != "2026-03-03 02:00:00" {
		t.Errorf("VariantsForItem = %+v (%v), want var1's stamp", vars, err)
	}
	groups, err := data.NewModifierRepo(d.DB).ListAllModifierGroupsWithAssignments(ctx)
	if err != nil || len(groups) != 1 || groups[0].UpdatedAt != "2026-03-03 03:00:00" {
		t.Errorf("ListAllModifierGroupsWithAssignments = %+v (%v), want grp1's stamp", groups, err)
	}
}
