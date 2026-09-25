package data_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// Catalog snapshot schema 2 (manage-shop catalog contract §3.7): every item,
// inactive included and ordered active first, with its full barcode set,
// direct modifier links, opt-outs, the till's own resolved (effective)
// groups, and its variants nested.
func TestCatalogSnapshotItems(t *testing.T) {
	f := newSaveFixture(t)
	ctx := context.Background()
	f.exec(t, `UPDATE items SET color = '#b45309', stock_untracked = 1 WHERE id = 'itm1'`)
	f.exec(t, `INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm-zz-off','SKU-OFF','Aardvark (old)',100,0)`)
	f.exec(t, `INSERT INTO item_barcodes (barcode, item_id, barcode_type, is_primary) VALUES ('B2','itm1','CODE128',0), ('B1','itm1','CODE128',1)`)
	f.exec(t, `INSERT INTO item_variants (id, item_id, sku, name, price, is_active) VALUES ('v-l','itm1','SKU1-L','Large',380,1), ('v-s','itm1','SKU1-S','Small',300,0)`)
	f.exec(t, `INSERT INTO variant_barcodes (barcode, variant_id, barcode_type, is_primary) VALUES ('VB1','v-l','CODE128',1)`)
	createAnchoredGroup(t, f.mods, "grp-syrup", "Syrup", 2)
	// itm1: direct Size; category Drinks links Milk + Syrup; opts out of Syrup.
	if err := f.mods.LinkGroupToItem(ctx, "itm1", "grp-size", 0); err != nil {
		t.Fatal(err)
	}
	if err := f.mods.SetCategoryModifierGroups(ctx, "cat1", []string{"grp-milk", "grp-syrup"}); err != nil {
		t.Fatal(err)
	}
	if err := f.mods.OptOutItemFromGroup(ctx, "itm1", "grp-syrup"); err != nil {
		t.Fatal(err)
	}

	items, err := f.catalog.CatalogSnapshotItems(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 4 {
		t.Fatalf("items = %d, want 4 (inactive included)", len(items))
	}
	if last := items[len(items)-1]; last.ID != "itm-zz-off" || last.Active {
		t.Fatalf("inactive item must sort last (active first), got %+v", last)
	}
	var it data.SnapshotItem
	for _, x := range items {
		if x.ID == "itm1" {
			it = x
		}
	}
	if it.Name != "Flat White" || it.SKU != "SKU1" || it.PriceMinor != 320 || it.CategoryID != "cat1" ||
		it.Color != "#b45309" || !it.Active || !it.StockUntracked {
		t.Fatalf("itm1 = %+v", it)
	}
	if !reflect.DeepEqual(it.Barcodes, []string{"B1", "B2"}) {
		t.Fatalf("barcodes = %v, want primary first", it.Barcodes)
	}
	if !reflect.DeepEqual(it.ModifierGroupIDs, []string{"grp-size"}) || !reflect.DeepEqual(it.ModifierOptOutIDs, []string{"grp-syrup"}) {
		t.Fatalf("links = %v opt-outs = %v", it.ModifierGroupIDs, it.ModifierOptOutIDs)
	}
	resolved, err := f.mods.ResolveGroupsForItem(ctx, "itm1")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{}
	for _, g := range resolved {
		want = append(want, g.ID)
	}
	if !reflect.DeepEqual(it.EffectiveModifierGroupIDs, want) || !reflect.DeepEqual(want, []string{"grp-size", "grp-milk"}) {
		t.Fatalf("effective = %v, resolver = %v", it.EffectiveModifierGroupIDs, want)
	}
	if len(it.Variants) != 2 || it.Variants[0].ID != "v-l" || !reflect.DeepEqual(it.Variants[0].Barcodes, []string{"VB1"}) ||
		it.Variants[0].SKU != "SKU1-L" || it.Variants[0].PriceMinor != 380 || !it.Variants[0].Active || it.Variants[1].Active {
		t.Fatalf("variants = %+v", it.Variants)
	}
	for _, x := range items {
		if x.Barcodes == nil || x.ModifierGroupIDs == nil || x.ModifierOptOutIDs == nil || x.EffectiveModifierGroupIDs == nil || x.Variants == nil {
			t.Fatalf("lists must be empty, never nil (the wire shape is [] not null): %+v", x)
		}
	}
}
