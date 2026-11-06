package data

import (
	"context"
	"reflect"
	"testing"
)

// Tester gap-fill (ut-docs#5588): description/starts_at/ends_at diff entries.
func TestUpdatePromotion_DiffDescriptionAndDates(t *testing.T) {
	_, repo := openPromoTestDB(t)
	ctx := context.Background()
	if err := repo.CreatePromotion(ctx, "P1", PromotionInput{Type: "amount", Value: 100}); err != nil {
		t.Fatal(err)
	}
	in := PromotionInput{Type: "amount", Value: 100, Description: "spring", StartsAt: "2031-01-02", EndsAt: "2031-02-03 23:59:59"}
	ch, err := repo.UpdatePromotion(ctx, "P1", in)
	if err != nil {
		t.Fatal(err)
	}
	want := PromotionChanges{
		"description": {From: "", To: "spring"},
		"starts_at":   {From: "", To: "2031-01-02"},
		"ends_at":     {From: "", To: "2031-02-03 23:59:59"},
	}
	if !reflect.DeepEqual(ch, want) {
		t.Fatalf("changes = %#v, want %#v", ch, want)
	}
}
