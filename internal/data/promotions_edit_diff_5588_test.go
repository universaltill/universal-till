package data

// ut-docs#5588: UpdatePromotion returns the stored-before/after diff of the
// edited columns, read inside the same tx as the UPDATE.

import (
	"context"
	"reflect"
	"testing"
)

func TestUpdatePromotion_DiffValueChange(t *testing.T) {
	_, repo := openPromoTestDB(t)
	ctx := context.Background()
	if err := repo.CreatePromotion(ctx, "P1", PromotionInput{Type: "amount", Value: 100}); err != nil {
		t.Fatal(err)
	}
	ch, err := repo.UpdatePromotion(ctx, "P1", PromotionInput{Type: "amount", Value: 250})
	if err != nil {
		t.Fatal(err)
	}
	want := PromotionChanges{"value": {From: int64(100), To: int64(250)}}
	if !reflect.DeepEqual(ch, want) {
		t.Fatalf("changes = %#v, want %#v", ch, want)
	}
}

func TestUpdatePromotion_DiffCustomerChange(t *testing.T) {
	_, repo := openPromoTestDB(t)
	ctx := context.Background()
	seedPromoCustomers(t, repo)
	if err := repo.CreatePromotion(ctx, "P1", PromotionInput{Type: "amount", Value: 100, CustomerID: "c-live"}); err != nil {
		t.Fatal(err)
	}
	ch, err := repo.UpdatePromotion(ctx, "P1", PromotionInput{Type: "amount", Value: 100, CustomerID: "c-other"})
	if err != nil {
		t.Fatal(err)
	}
	want := PromotionChanges{"customer_id": {From: "c-live", To: "c-other"}}
	if !reflect.DeepEqual(ch, want) {
		t.Fatalf("rebind changes = %#v, want %#v", ch, want)
	}
	ch, err = repo.UpdatePromotion(ctx, "P1", PromotionInput{Type: "amount", Value: 100, CustomerID: ""})
	if err != nil {
		t.Fatal(err)
	}
	want = PromotionChanges{"customer_id": {From: "c-other", To: ""}}
	if !reflect.DeepEqual(ch, want) {
		t.Fatalf("clear changes = %#v, want %#v", ch, want)
	}
}

func TestUpdatePromotion_DiffKeepCustomerOmitsCustomer(t *testing.T) {
	_, repo := openPromoTestDB(t)
	ctx := context.Background()
	seedPromoCustomers(t, repo)
	if err := repo.CreatePromotion(ctx, "P1", PromotionInput{Type: "amount", Value: 100, CustomerID: "c-live"}); err != nil {
		t.Fatal(err)
	}
	ch, err := repo.UpdatePromotion(ctx, "P1", PromotionInput{Type: "amount", Value: 100, KeepCustomer: true, CustomerID: "c-other"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ch["customer_id"]; ok || len(ch) != 0 {
		t.Fatalf("keep: changes = %#v, want none", ch)
	}
	seedDanglingPromo(t, repo, "P-GONE", "c-gone")
	ch, err = repo.UpdatePromotion(ctx, "P-GONE", PromotionInput{Type: "amount", Value: 150, CustomerID: "c-gone"})
	if err != nil {
		t.Fatal(err)
	}
	want := PromotionChanges{"value": {From: int64(100), To: int64(150)}}
	if !reflect.DeepEqual(ch, want) {
		t.Fatalf("dangling changes = %#v, want %#v", ch, want)
	}
}

func TestUpdatePromotion_DiffTypeChangeRecordsBothUnits(t *testing.T) {
	_, repo := openPromoTestDB(t)
	ctx := context.Background()
	if err := repo.CreatePromotion(ctx, "P1", PromotionInput{Type: "percent", Value: 1000}); err != nil {
		t.Fatal(err)
	}
	ch, err := repo.UpdatePromotion(ctx, "P1", PromotionInput{Type: "amount", Value: 500})
	if err != nil {
		t.Fatal(err)
	}
	want := PromotionChanges{
		"type":  {From: "percent", To: "amount"},
		"value": {From: int64(1000), To: int64(500)},
	}
	if !reflect.DeepEqual(ch, want) {
		t.Fatalf("changes = %#v, want %#v", ch, want)
	}
}

func TestUpdatePromotion_DiffNoOpIsEmptyNonNil(t *testing.T) {
	_, repo := openPromoTestDB(t)
	ctx := context.Background()
	// Description/dates are NULL in the row; "" input must not be reported.
	if err := repo.CreatePromotion(ctx, "P1", PromotionInput{Type: "amount", Value: 100}); err != nil {
		t.Fatal(err)
	}
	ch, err := repo.UpdatePromotion(ctx, "P1", PromotionInput{Type: "amount", Value: 100})
	if err != nil {
		t.Fatal(err)
	}
	if ch == nil || len(ch) != 0 {
		t.Fatalf("changes = %#v, want empty non-nil", ch)
	}
}

func TestUpdatePromotion_UnknownCodeReturnsNilChanges(t *testing.T) {
	_, repo := openPromoTestDB(t)
	ch, err := repo.UpdatePromotion(context.Background(), "MISSING", PromotionInput{Type: "amount", Value: 1})
	if err == nil || ch != nil {
		t.Fatalf("changes=%#v err=%v, want nil changes and an error", ch, err)
	}
}
