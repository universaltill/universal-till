package data_test

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#4348: switching an item off retires its variants; switching it
// back on must revive exactly the variants the switch-off retired
// (item_variants.retired_with_item = 1), never one that was already off on
// its own.

// seedReviveVariants gives itm1 two live variants and one already switched
// off on its own.
func seedReviveVariants(t *testing.T, f saveFixture) {
	t.Helper()
	f.exec(t, `INSERT INTO item_variants (id, item_id, sku, name, price, is_active) VALUES
		('v-small', 'itm1', 'FW-S', 'Small', 300, 1),
		('v-large', 'itm1', 'FW-L', 'Large', 380, 1),
		('v-off',   'itm1', 'FW-X', 'Extra', 420, 0)`)
}

// variantStates is "<id>:<is_active>/<retired_with_item>" per itm1 variant,
// ordered by id.
func variantStates(t *testing.T, f saveFixture) []string {
	t.Helper()
	return f.col(t, `SELECT id || ':' || is_active || '/' || retired_with_item FROM item_variants WHERE item_id = 'itm1' ORDER BY id`)
}

func wantStates(t *testing.T, step string, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: variants = %v, want %v", step, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s: variants = %v, want %v", step, got, want)
		}
	}
}

func TestDeactivateItemThenSaveItemOn_RevivesOnlyCascadeRetiredVariants_4348(t *testing.T) {
	f := newSaveFixture(t)
	ctx := context.Background()
	seedReviveVariants(t, f)

	if err := f.catalog.DeactivateItem(ctx, "itm1"); err != nil {
		t.Fatal(err)
	}
	wantStates(t, "after DeactivateItem", variantStates(t, f), "v-large:0/1", "v-off:0/0", "v-small:0/1")

	if _, err := f.catalog.SaveItem(ctx, data.ItemPatch{ID: "itm1", Active: boolp(true)}); err != nil {
		t.Fatal(err)
	}
	wantStates(t, "after SaveItem(on)", variantStates(t, f), "v-large:1/0", "v-off:0/0", "v-small:1/0")
}

func TestSaveItemOffOn_RevivesOnlyCascadeRetiredVariants_4348(t *testing.T) {
	f := newSaveFixture(t)
	ctx := context.Background()
	seedReviveVariants(t, f)

	if _, err := f.catalog.SaveItem(ctx, data.ItemPatch{ID: "itm1", Active: boolp(false)}); err != nil {
		t.Fatal(err)
	}
	wantStates(t, "after SaveItem(off)", variantStates(t, f), "v-large:0/1", "v-off:0/0", "v-small:0/1")

	// Saving the still-off item again (a rename, say) changes nothing.
	if _, err := f.catalog.SaveItem(ctx, data.ItemPatch{ID: "itm1", Name: strp("Flat White 2"), Active: boolp(false)}); err != nil {
		t.Fatal(err)
	}
	wantStates(t, "after SaveItem(off again)", variantStates(t, f), "v-large:0/1", "v-off:0/0", "v-small:0/1")

	if _, err := f.catalog.SaveItem(ctx, data.ItemPatch{ID: "itm1", Active: boolp(true)}); err != nil {
		t.Fatal(err)
	}
	wantStates(t, "after SaveItem(on)", variantStates(t, f), "v-large:1/0", "v-off:0/0", "v-small:1/0")

	// Saving the live item on again revives nothing further.
	if _, err := f.catalog.SaveItem(ctx, data.ItemPatch{ID: "itm1", Active: boolp(true)}); err != nil {
		t.Fatal(err)
	}
	wantStates(t, "after SaveItem(on again)", variantStates(t, f), "v-large:1/0", "v-off:0/0", "v-small:1/0")
}

func TestUpdateItemReturningWasActive_CascadesBothWays_4348(t *testing.T) {
	f := newSaveFixture(t)
	ctx := context.Background()
	seedReviveVariants(t, f)

	set := func(active bool) bool {
		t.Helper()
		in, ok, err := f.catalog.GetItem(ctx, "itm1")
		if err != nil || !ok {
			t.Fatalf("GetItem: ok=%v err=%v", ok, err)
		}
		in.IsActive = active
		was, err := f.catalog.UpdateItemReturningWasActive(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		return was
	}

	if was := set(false); !was {
		t.Fatal("switch-off: wasActive = false, want true")
	}
	wantStates(t, "after editor switch-off", variantStates(t, f), "v-large:0/1", "v-off:0/0", "v-small:0/1")

	if was := set(true); was {
		t.Fatal("switch-on: wasActive = true, want false")
	}
	wantStates(t, "after editor switch-on", variantStates(t, f), "v-large:1/0", "v-off:0/0", "v-small:1/0")

	// An ordinary edit of a live item touches no variant.
	set(true)
	wantStates(t, "after editor re-save", variantStates(t, f), "v-large:1/0", "v-off:0/0", "v-small:1/0")
}

// An explicit per-variant on/off while its item is off decides that
// variant's fate itself: switching the item back on must not override it.
func TestExplicitVariantActiveWritesClearRetiredWithItem_4348(t *testing.T) {
	f := newSaveFixture(t)
	ctx := context.Background()
	seedReviveVariants(t, f)
	f.exec(t, `INSERT INTO item_variants (id, item_id, sku, name, price, is_active) VALUES
		('v-tall',  'itm1', 'FW-T', 'Tall', 400, 1),
		('v-cup',   'itm1', 'FW-C', 'Cup',  250, 1),
		('v-mug',   'itm1', 'FW-M', 'Mug',  260, 1)`)

	if err := f.catalog.DeactivateItem(ctx, "itm1"); err != nil {
		t.Fatal(err)
	}
	// DeactivateVariant: the operator removes v-small for good.
	if err := f.catalog.DeactivateVariant(ctx, "v-small"); err != nil {
		t.Fatal(err)
	}
	// save_item_variant echoing active=false on a variant already off
	// (my. sends the form's state on every edit): no change, so the flag
	// stays and it still comes back with its item.
	if _, err := f.catalog.SaveVariant(ctx, data.VariantSave{ItemID: "itm1", ID: "v-large", Active: boolp(false)}); err != nil {
		t.Fatal(err)
	}
	// save_item_variant with only a price: no say on active, so it still
	// comes back with its item.
	if _, err := f.catalog.SaveVariant(ctx, data.VariantSave{ItemID: "itm1", ID: "v-tall", PriceMinor: i64p(410)}); err != nil {
		t.Fatal(err)
	}
	// The till editor's variant form: a price edit echoing the unticked
	// box keeps the flag; ticking the box switches it on now and clears it.
	if err := f.catalog.UpdateVariant(ctx, catalogtypes.VariantInput{ID: "v-cup", ItemID: "itm1", Name: "Cup", Price: 255, IsActive: false}); err != nil {
		t.Fatal(err)
	}
	if err := f.catalog.UpdateVariant(ctx, catalogtypes.VariantInput{ID: "v-mug", ItemID: "itm1", Name: "Mug", Price: 260, IsActive: true}); err != nil {
		t.Fatal(err)
	}
	wantStates(t, "after explicit variant writes", variantStates(t, f),
		"v-cup:0/1", "v-large:0/1", "v-mug:1/0", "v-off:0/0", "v-small:0/0", "v-tall:0/1")

	if _, err := f.catalog.SaveItem(ctx, data.ItemPatch{ID: "itm1", Active: boolp(true)}); err != nil {
		t.Fatal(err)
	}
	wantStates(t, "after item switch-on", variantStates(t, f),
		"v-cup:1/0", "v-large:1/0", "v-mug:1/0", "v-off:0/0", "v-small:0/0", "v-tall:1/0")

	// save_item_variant switching a live variant off is an explicit switch:
	// the flag clears, so it stays off through the next off/on cycle.
	if _, err := f.catalog.SaveVariant(ctx, data.VariantSave{ItemID: "itm1", ID: "v-large", Active: boolp(false)}); err != nil {
		t.Fatal(err)
	}
	if err := f.catalog.DeactivateItem(ctx, "itm1"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.catalog.SaveItem(ctx, data.ItemPatch{ID: "itm1", Active: boolp(true)}); err != nil {
		t.Fatal(err)
	}
	if got := f.str(t, `SELECT is_active || '/' || retired_with_item FROM item_variants WHERE id = 'v-large'`); got != "0/0" {
		t.Fatalf("v-large after explicit save_item_variant off + item off/on = %s, want 0/0", got)
	}

	// A variant reactivated explicitly later is a plain live variant.
	if _, err := f.catalog.SaveVariant(ctx, data.VariantSave{ItemID: "itm1", ID: "v-small", Active: boolp(true)}); err != nil {
		t.Fatal(err)
	}
	if got := f.str(t, `SELECT is_active || '/' || retired_with_item FROM item_variants WHERE id = 'v-small'`); got != "1/0" {
		t.Fatalf("v-small after explicit reactivation = %s, want 1/0", got)
	}
}
