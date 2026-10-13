package data_test

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// ut-docs#4214: items.item_type ('standard' | 'gift_voucher') round-trips
// through CreateItem / GetItem / ListItems / UpdateItem / ExportRows /
// CatalogSnapshotItems.
func TestCatalogRepo_ItemTypeRoundTrip(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	id, err := repo.CreateItem(ctx, catalogtypes.ItemInput{Name: "Gift voucher", SKU: "GV", Unit: "each", BasePrice: 0, IsActive: true, ItemType: catalogtypes.ItemTypeGiftVoucher})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	plain, err := repo.CreateItem(ctx, catalogtypes.ItemInput{Name: "Bread", SKU: "BR", Unit: "each", BasePrice: 200, IsActive: true})
	if err != nil {
		t.Fatalf("create plain: %v", err)
	}
	got, ok, err := repo.GetItem(ctx, id)
	if err != nil || !ok || got.ItemType != catalogtypes.ItemTypeGiftVoucher {
		t.Fatalf("GetItem: want gift_voucher, got %+v ok=%v err=%v", got.ItemType, ok, err)
	}
	if p, _, _ := repo.GetItem(ctx, plain); p.ItemType != catalogtypes.ItemTypeStandard {
		t.Fatalf("an item created with no type must read standard, got %q", p.ItemType)
	}
	list, err := repo.ListItems(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range list {
		want := catalogtypes.ItemTypeStandard
		if it.ID == id {
			want = catalogtypes.ItemTypeGiftVoucher
		}
		if it.ItemType != want {
			t.Fatalf("ListItems %s: item_type %q, want %q", it.Name, it.ItemType, want)
		}
	}
	rows, err := repo.ExportRows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Name == "Gift voucher" && r.ItemType != catalogtypes.ItemTypeGiftVoucher {
			t.Fatalf("ExportRows: want gift_voucher, got %q", r.ItemType)
		}
	}
	snap, err := repo.CatalogSnapshotItems(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range snap {
		if s.ID == id && s.ItemType != catalogtypes.ItemTypeGiftVoucher {
			t.Fatalf("snapshot: want gift_voucher, got %q", s.ItemType)
		}
	}

	got.ItemType = catalogtypes.ItemTypeStandard
	got.BasePrice = 250
	if err := repo.UpdateItem(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _, _ = repo.GetItem(ctx, id)
	if got.ItemType != catalogtypes.ItemTypeStandard || got.BasePrice != 250 {
		t.Fatalf("after update: %+v", got)
	}
	// An unknown type is refused before it reaches the CHECK.
	got.ItemType = "deposit"
	if err := repo.UpdateItem(ctx, got); err == nil {
		t.Fatal("an unknown item type must be refused")
	}
}

// save_item's item_type sets the column on create and update; absent keeps
// it; an unknown value is refused.
func TestSaveItem_ItemType(t *testing.T) {
	f := newSaveFixture(t)
	ctx := context.Background()
	id := "33333333-3333-4333-8333-333333333333"
	gv := catalogtypes.ItemTypeGiftVoucher
	if _, err := f.catalog.SaveItem(ctx, data.ItemPatch{ID: id, Create: true, Name: strp("Gift voucher"), PriceMinor: i64p(0), ItemType: &gv}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := f.str(t, `SELECT item_type FROM items WHERE id = ?`, id); got != gv {
		t.Fatalf("after create: %q", got)
	}
	if _, err := f.catalog.SaveItem(ctx, data.ItemPatch{ID: id, Name: strp("Voucher 2")}); err != nil {
		t.Fatalf("update name: %v", err)
	}
	if got := f.str(t, `SELECT item_type FROM items WHERE id = ?`, id); got != gv {
		t.Fatalf("absent must keep: %q", got)
	}
	std := catalogtypes.ItemTypeStandard
	res, err := f.catalog.SaveItem(ctx, data.ItemPatch{ID: id, ItemType: &std})
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got := f.str(t, `SELECT item_type FROM items WHERE id = ?`, id); got != std {
		t.Fatalf("after set standard: %q", got)
	}
	found := false
	for _, c := range res.Changed {
		found = found || c == "item_type"
	}
	if !found {
		t.Fatalf("Changed must list item_type: %v", res.Changed)
	}
	bad := "deposit"
	if _, err := f.catalog.SaveItem(ctx, data.ItemPatch{ID: id, ItemType: &bad}); err == nil {
		t.Fatal("an unknown item type must be refused")
	}
}
