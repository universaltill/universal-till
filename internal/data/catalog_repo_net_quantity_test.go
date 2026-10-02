package data_test

// ut-docs#3391: a pre-packed item's net quantity (value + g/ml/ea unit)
// round-trips through CreateItem/UpdateItem/GetItem/ListItems, reaches
// GetItemLabel for the shelf-label unit price, and an invalid pair is
// refused at the repo layer (not just by the form handler).

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/testsupport"
)

func sptr(s string) *string { return &s }

func TestGetItemLabel_CarriesNetQuantityAndIsWeighed(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	rice, err := repo.CreateItem(ctx, catalogtypes.ItemInput{
		SKU: "RICE", Name: "Rice 500g", BasePrice: 200, IsActive: true,
		NetQuantityValue: i64p(500), NetQuantityUnit: sptr("g"),
	})
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}
	plain, err := repo.CreateItem(ctx, catalogtypes.ItemInput{SKU: "MUG", Name: "Mug", BasePrice: 500, IsActive: true})
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}
	bananas, err := repo.CreateItem(ctx, catalogtypes.ItemInput{SKU: "BAN", Name: "Bananas", BasePrice: 150, IsActive: true, IsWeighed: true})
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}

	l, ok, err := repo.GetItemLabel(ctx, rice)
	if err != nil || !ok {
		t.Fatalf("GetItemLabel: ok=%v err=%v", ok, err)
	}
	if !l.NetQuantityValue.Valid || l.NetQuantityValue.Int64 != 500 || !l.NetQuantityUnit.Valid || l.NetQuantityUnit.String != "g" {
		t.Fatalf("rice label net quantity = %+v / %+v, want 500 g", l.NetQuantityValue, l.NetQuantityUnit)
	}
	if l.IsWeighed {
		t.Fatal("rice must not read as weighed")
	}

	l, _, _ = repo.GetItemLabel(ctx, plain)
	if l.NetQuantityValue.Valid || l.NetQuantityUnit.Valid {
		t.Fatalf("an item with no net quantity must read NULL/NULL, got %+v / %+v", l.NetQuantityValue, l.NetQuantityUnit)
	}

	l, _, _ = repo.GetItemLabel(ctx, bananas)
	if !l.IsWeighed {
		t.Fatal("bananas must read as weighed")
	}
}

func TestNetQuantity_RoundTripsAndClears(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	id, err := repo.CreateItem(ctx, catalogtypes.ItemInput{SKU: "OIL", Name: "Olive oil", BasePrice: 600, IsActive: true})
	if err != nil {
		t.Fatal(err)
	}
	got, _, _ := repo.GetItem(ctx, id)
	if got.NetQuantityValue != nil || got.NetQuantityUnit != nil {
		t.Fatalf("fresh item: got %v/%v, want nil/nil", got.NetQuantityValue, got.NetQuantityUnit)
	}

	got.NetQuantityValue, got.NetQuantityUnit = i64p(750), sptr("ml")
	if _, err := repo.UpdateItemReturningWasActive(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _, _ = repo.GetItem(ctx, id)
	if got.NetQuantityValue == nil || *got.NetQuantityValue != 750 || got.NetQuantityUnit == nil || *got.NetQuantityUnit != "ml" {
		t.Fatalf("after set: got %v/%v, want 750 ml", got.NetQuantityValue, got.NetQuantityUnit)
	}
	items, err := repo.ListItems(ctx)
	if err != nil || len(items) != 1 || items[0].NetQuantityValue == nil || *items[0].NetQuantityValue != 750 {
		t.Fatalf("ListItems: %+v err=%v", items, err)
	}

	// A partial update (another field only) must keep the net quantity —
	// it is read-modify-write through getItemExec.
	if ok, err := repo.UpdateItemPartial(ctx, id, nil, sptr("Extra virgin"), nil, nil, nil, nil); err != nil || !ok {
		t.Fatalf("partial: ok=%v err=%v", ok, err)
	}
	got, _, _ = repo.GetItem(ctx, id)
	if got.NetQuantityValue == nil || *got.NetQuantityValue != 750 {
		t.Fatalf("partial update dropped the net quantity: %v", got.NetQuantityValue)
	}

	got.NetQuantityValue, got.NetQuantityUnit = nil, nil
	if err := repo.UpdateItem(ctx, got); err != nil {
		t.Fatalf("clear: %v", err)
	}
	got, _, _ = repo.GetItem(ctx, id)
	if got.NetQuantityValue != nil || got.NetQuantityUnit != nil {
		t.Fatalf("after clear: got %v/%v, want nil/nil", got.NetQuantityValue, got.NetQuantityUnit)
	}
}

func TestNetQuantity_InvalidPairRefused(t *testing.T) {
	db := testsupport.NewCatalogTestDB(t)
	repo := data.NewCatalogRepo(db)
	ctx := context.Background()

	bad := []struct {
		name string
		v    *int64
		u    *string
	}{
		{"unknown unit", i64p(500), sptr("kg")},
		{"zero value", i64p(0), sptr("g")},
		{"negative value", i64p(-5), sptr("g")},
		{"value without unit", i64p(500), nil},
		{"unit without value", nil, sptr("ml")},
	}
	for _, tc := range bad {
		if _, err := repo.CreateItem(ctx, catalogtypes.ItemInput{Name: "X " + tc.name, BasePrice: 100, IsActive: true, NetQuantityValue: tc.v, NetQuantityUnit: tc.u}); err == nil {
			t.Errorf("CreateItem %s: want an error, got nil", tc.name)
		}
	}
	id, err := repo.CreateItem(ctx, catalogtypes.ItemInput{SKU: "OK", Name: "Ok", BasePrice: 100, IsActive: true})
	if err != nil {
		t.Fatal(err)
	}
	in, _, _ := repo.GetItem(ctx, id)
	in.NetQuantityValue, in.NetQuantityUnit = i64p(6), sptr("pack")
	if _, err := repo.UpdateItemReturningWasActive(ctx, in); err == nil {
		t.Fatal("update with unit 'pack': want an error, got nil")
	}
}
