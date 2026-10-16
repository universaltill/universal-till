package data

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

// ut-docs#4491: the Tx variants the cloud catalogue pull applies modifier
// groups, option sets and their link lists through.

type pullTxFixture struct {
	t    *testing.T
	ctx  context.Context
	db   *sql.DB
	cat  *CatalogRepo
	mods *ModifierRepo
	sets *OptionSetRepo
}

func newPullTxFixture(t *testing.T) pullTxFixture {
	t.Helper()
	d := openMigratedDB(t, "pull-tx-4491.db")
	f := pullTxFixture{t: t, ctx: context.Background(), db: d.DB, cat: NewCatalogRepo(d.DB), mods: NewModifierRepo(d.DB), sets: NewOptionSetRepo(d.DB)}
	name, price := "Latte", int64(300)
	if _, err := f.cat.SaveItem(f.ctx, ItemPatch{ID: "itm-1", Create: true, Name: &name, PriceMinor: &price}); err != nil {
		t.Fatal(err)
	}
	cname := "Coffee"
	if _, err := f.cat.SaveCategory(f.ctx, CategorySave{ID: "cat-1", Create: true, Name: &cname}); err != nil {
		t.Fatal(err)
	}
	return f
}

// inTx runs fn in a transaction, committing only when fn succeeds.
func (f pullTxFixture) inTx(fn func(tx *sql.Tx) error) error {
	f.t.Helper()
	tx, err := f.db.BeginTx(f.ctx, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (f pullTxFixture) count(q string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.db.QueryRow(q, args...).Scan(&n); err != nil {
		f.t.Fatalf("%s: %v", q, err)
	}
	return n
}

func TestSaveGroupTx_SortOrderActiveAndRules(t *testing.T) {
	f := newPullTxFixture(t)
	name, sort, off := "Milk", 4, false
	opts := []ModifierOption{{ID: "o1", Name: "Oat", PriceDeltaMinor: 40, IsActive: true}}
	var res ModifierGroupSaveResult
	err := f.inTx(func(tx *sql.Tx) error {
		var err error
		res, err = f.mods.SaveGroupTx(f.ctx, tx, ModifierGroupSave{ID: "g1", Create: true, Name: &name, SortOrder: &sort, Active: &off, Options: &opts})
		return err
	})
	if err != nil || !res.Created || res.Name != "Milk" {
		t.Fatalf("create = %+v %v", res, err)
	}
	if n := f.count(`SELECT COUNT(*) FROM item_modifier_groups WHERE id = 'g1' AND sort_order = 4 AND is_active = 0`); n != 1 {
		t.Fatal("sort_order / active not stored")
	}
	if n := f.count(`SELECT COUNT(*) FROM item_modifier_options WHERE group_id = 'g1'`); n != 1 {
		t.Fatal("options not stored")
	}

	// An active group of the same name blocks reactivating g1.
	other := "milk"
	if _, err := f.mods.SaveGroup(f.ctx, ModifierGroupSave{ID: "g2", Create: true, Name: &other}); err != nil {
		t.Fatal(err)
	}
	on := true
	err = f.inTx(func(tx *sql.Tx) error {
		_, err := f.mods.SaveGroupTx(f.ctx, tx, ModifierGroupSave{ID: "g1", Active: &on})
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("reactivating onto a taken name = %v, want refused", err)
	}

	neg := -1
	for _, p := range []ModifierGroupSave{
		{ID: "g1", SortOrder: &neg},
		{ID: "g1", MinSelect: intPtr(3), MaxSelect: intPtr(2)},
		{ID: "missing", Name: &name},
	} {
		err := f.inTx(func(tx *sql.Tx) error {
			_, err := f.mods.SaveGroupTx(f.ctx, tx, p)
			return err
		})
		if err == nil {
			t.Fatalf("SaveGroupTx(%+v) accepted", p)
		}
	}
	if n := f.count(`SELECT COUNT(*) FROM item_modifier_groups WHERE id = 'g1' AND sort_order = 4 AND min_select = 0 AND max_select = 1`); n != 1 {
		t.Fatal("a refused save wrote")
	}

	// SaveGroup without the pull-only fields keeps both.
	rename := "Milk 2"
	if _, err := f.mods.SaveGroup(f.ctx, ModifierGroupSave{ID: "g1", Name: &rename}); err != nil {
		t.Fatal(err)
	}
	if n := f.count(`SELECT COUNT(*) FROM item_modifier_groups WHERE id = 'g1' AND name = 'Milk 2' AND sort_order = 4 AND is_active = 0`); n != 1 {
		t.Fatal("SaveGroup without SortOrder/Active changed them")
	}
}

func intPtr(n int) *int { return &n }

func TestDeleteGroupIfExistsTx_CascadesAndReportsReplay(t *testing.T) {
	f := newPullTxFixture(t)
	name := "Milk"
	opts := []ModifierOption{{ID: "o1", Name: "Oat", IsActive: true}}
	if _, err := f.mods.SaveGroup(f.ctx, ModifierGroupSave{ID: "g1", Create: true, Name: &name, Options: &opts, AttachItemIDs: []string{"itm-1"}, AttachCategoryIDs: []string{"cat-1"}}); err != nil {
		t.Fatal(err)
	}
	for i, want := range []bool{true, false} {
		var got bool
		if err := f.inTx(func(tx *sql.Tx) error {
			var err error
			got, err = f.mods.DeleteGroupIfExistsTx(f.ctx, tx, "g1")
			return err
		}); err != nil || got != want {
			t.Fatalf("delete #%d = %v %v, want %v", i+1, got, err, want)
		}
	}
	if n := f.count(`SELECT COUNT(*) FROM item_modifier_options WHERE group_id = 'g1'`) +
		f.count(`SELECT COUNT(*) FROM item_modifier_group_links WHERE group_id = 'g1'`) +
		f.count(`SELECT COUNT(*) FROM category_modifier_group_links WHERE group_id = 'g1'`); n != 0 {
		t.Fatalf("%d dependent rows survived", n)
	}
}

func TestSetCategoryModifierGroupsTx(t *testing.T) {
	f := newPullTxFixture(t)
	for _, id := range []string{"g1", "g2"} {
		name := "Group " + id
		if _, err := f.mods.SaveGroup(f.ctx, ModifierGroupSave{ID: id, Create: true, Name: &name}); err != nil {
			t.Fatal(err)
		}
	}
	// An inactive group is still a valid link (the cloud's list is the set).
	off := false
	if err := f.inTx(func(tx *sql.Tx) error {
		_, err := f.mods.SaveGroupTx(f.ctx, tx, ModifierGroupSave{ID: "g1", Active: &off})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.inTx(func(tx *sql.Tx) error {
		return f.mods.SetCategoryModifierGroupsTx(f.ctx, tx, "cat-1", []string{"g2", "g1"})
	}); err != nil {
		t.Fatal(err)
	}
	if n := f.count(`SELECT COUNT(*) FROM category_modifier_group_links WHERE category_id = 'cat-1' AND ((group_id = 'g2' AND sort_order = 0) OR (group_id = 'g1' AND sort_order = 1))`); n != 2 {
		t.Fatal("links not set in order")
	}
	for _, c := range []struct {
		cat string
		ids []string
	}{{"cat-1", []string{"g2", "nope"}}, {"no-such-cat", []string{"g1"}}} {
		err := f.inTx(func(tx *sql.Tx) error { return f.mods.SetCategoryModifierGroupsTx(f.ctx, tx, c.cat, c.ids) })
		if err == nil {
			t.Fatalf("SetCategoryModifierGroupsTx(%s, %v) accepted", c.cat, c.ids)
		}
	}
	if n := f.count(`SELECT COUNT(*) FROM category_modifier_group_links WHERE category_id = 'cat-1'`); n != 2 {
		t.Fatal("a refused set changed the links")
	}
	if err := f.inTx(func(tx *sql.Tx) error { return f.mods.SetCategoryModifierGroupsTx(f.ctx, tx, "cat-1", []string{}) }); err != nil {
		t.Fatal(err)
	}
	if n := f.count(`SELECT COUNT(*) FROM category_modifier_group_links WHERE category_id = 'cat-1'`); n != 0 {
		t.Fatal("an empty set did not clear")
	}
	// The non-Tx method is unchanged.
	if err := f.mods.SetCategoryModifierGroups(f.ctx, "cat-1", []string{"g1", " ", "g1"}); err != nil {
		t.Fatal(err)
	}
	if n := f.count(`SELECT COUNT(*) FROM category_modifier_group_links WHERE category_id = 'cat-1'`); n != 1 {
		t.Fatal("SetCategoryModifierGroups changed behaviour")
	}
}

func TestOptionSetTxVariants(t *testing.T) {
	f := newPullTxFixture(t)
	name := "Size"
	vals := []OptionSetValueInput{{ID: "v1", Value: "S"}, {ID: "v2", Value: "M"}}
	var res OptionSetSaveResult
	if err := f.inTx(func(tx *sql.Tx) error {
		var err error
		res, err = f.sets.SaveOptionSetTx(f.ctx, tx, OptionSetSave{ID: "s1", Create: true, Name: &name, Values: &vals})
		return err
	}); err != nil || !res.Created {
		t.Fatalf("create = %+v %v", res, err)
	}
	dup := []OptionSetValueInput{{ID: "v1", Value: "S"}, {ID: "v3", Value: "S"}}
	if err := f.inTx(func(tx *sql.Tx) error {
		_, err := f.sets.SaveOptionSetTx(f.ctx, tx, OptionSetSave{ID: "s1", Values: &dup})
		return err
	}); !errors.Is(err, ErrOptionSetValueExists) {
		t.Fatalf("duplicate value = %v", err)
	}
	if n := f.count(`SELECT COUNT(*) FROM option_set_values WHERE option_set_id = 's1'`); n != 2 {
		t.Fatal("refused save wrote")
	}

	// Apply to the item (no variants are generated), then the delete rule.
	if err := f.inTx(func(tx *sql.Tx) error { return f.sets.ApplyOptionSetsToItemTx(f.ctx, tx, "itm-1", []string{"s1"}) }); err != nil {
		t.Fatal(err)
	}
	if n := f.count(`SELECT COUNT(*) FROM item_option_sets WHERE item_id = 'itm-1' AND option_set_id = 's1'`); n != 1 {
		t.Fatal("not applied")
	}
	if n := f.count(`SELECT COUNT(*) FROM item_variants WHERE item_id = 'itm-1'`); n != 0 {
		t.Fatal("ApplyOptionSetsToItemTx generated variants")
	}
	for _, ids := range [][]string{{"s1", "s1"}, {"nope"}} {
		if err := f.inTx(func(tx *sql.Tx) error { return f.sets.ApplyOptionSetsToItemTx(f.ctx, tx, "itm-1", ids) }); err == nil {
			t.Fatalf("ApplyOptionSetsToItemTx(%v) accepted", ids)
		}
	}
	if err := f.inTx(func(tx *sql.Tx) error {
		_, err := f.sets.DeleteOptionSetIfUnusedTx(f.ctx, tx, "s1")
		return err
	}); !errors.Is(err, ErrOptionSetInUse) {
		t.Fatalf("delete in use = %v", err)
	}
	if err := f.inTx(func(tx *sql.Tx) error { return f.sets.ApplyOptionSetsToItemTx(f.ctx, tx, "itm-1", nil) }); err != nil {
		t.Fatal(err)
	}
	for i, want := range []bool{true, false} {
		var got bool
		if err := f.inTx(func(tx *sql.Tx) error {
			var err error
			got, err = f.sets.DeleteOptionSetIfUnusedTx(f.ctx, tx, "s1")
			return err
		}); err != nil || got != want {
			t.Fatalf("delete #%d = %v %v, want %v", i+1, got, err, want)
		}
	}
}
