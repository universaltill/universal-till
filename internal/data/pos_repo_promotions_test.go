package data

// Promotions management (ut-docs#634): create/edit/deactivate/list promo
// codes from the product, alongside the existing checkout-time
// FindActivePromo lookup. Soft-delete only (is_active) -- history is never
// hard-deleted.

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
)

func openPromoTestDB(t *testing.T) (*db.DB, *POSRepo) {
	t.Helper()
	dbo, err := db.Open(filepath.Join(t.TempDir(), "promo_admin.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { dbo.Close() })
	return dbo, NewPOSRepo(dbo.DB)
}

func TestCreatePromotion_AmountRoundTripsAsMinorUnits(t *testing.T) {
	_, repo := openPromoTestDB(t)
	ctx := context.Background()

	if err := repo.CreatePromotion(ctx, "SAVE5", PromotionInput{
		Type:        "amount",
		Value:       500, // £5.00 in minor units
		Description: "£5 off",
	}); err != nil {
		t.Fatalf("CreatePromotion: %v", err)
	}

	list, err := repo.ListPromotionsForAdmin(ctx)
	if err != nil {
		t.Fatalf("ListPromotionsForAdmin: %v", err)
	}
	found := false
	for _, p := range list {
		if p.Code == "SAVE5" {
			found = true
			if p.Type != "amount" || p.Value != 500 {
				t.Fatalf("amount promo did not round-trip: %+v", p)
			}
			if !p.IsActive {
				t.Fatal("new promotion should be active by default")
			}
		}
	}
	if !found {
		t.Fatal("SAVE5 not present in ListPromotionsForAdmin")
	}
}

func TestCreatePromotion_PercentRoundTripsAsBasisPoints(t *testing.T) {
	_, repo := openPromoTestDB(t)
	ctx := context.Background()

	if err := repo.CreatePromotion(ctx, "TENOFF", PromotionInput{
		Type:  "percent",
		Value: 1000, // 10% => 1000 basis points, matching DISC10's seed row
	}); err != nil {
		t.Fatalf("CreatePromotion: %v", err)
	}

	list, err := repo.ListPromotionsForAdmin(ctx)
	if err != nil {
		t.Fatalf("ListPromotionsForAdmin: %v", err)
	}
	for _, p := range list {
		if p.Code == "TENOFF" {
			if p.Type != "percent" || p.Value != 1000 {
				t.Fatalf("percent promo did not round-trip as basis points: %+v", p)
			}
			return
		}
	}
	t.Fatal("TENOFF not present in ListPromotionsForAdmin")
}

func TestCreatePromotion_DuplicateCodeIsDistinctError(t *testing.T) {
	_, repo := openPromoTestDB(t)
	ctx := context.Background()

	in := PromotionInput{Type: "amount", Value: 100}
	if err := repo.CreatePromotion(ctx, "DUPCODE", in); err != nil {
		t.Fatalf("first create: %v", err)
	}
	err := repo.CreatePromotion(ctx, "DUPCODE", in)
	if err == nil {
		t.Fatal("duplicate code must error")
	}
	if err != ErrPromotionCodeExists {
		t.Fatalf("duplicate code must return the distinct ErrPromotionCodeExists, got: %v", err)
	}
}

func TestListPromotionsForAdmin_IncludesActiveAndInactive(t *testing.T) {
	_, repo := openPromoTestDB(t)
	ctx := context.Background()

	if err := repo.CreatePromotion(ctx, "ACTIVE1", PromotionInput{Type: "amount", Value: 100}); err != nil {
		t.Fatalf("create active: %v", err)
	}
	if err := repo.CreatePromotion(ctx, "INACTIVE1", PromotionInput{Type: "amount", Value: 100}); err != nil {
		t.Fatalf("create inactive: %v", err)
	}
	if err := repo.SetPromotionActive(ctx, "INACTIVE1", false); err != nil {
		t.Fatalf("deactivate: %v", err)
	}

	list, err := repo.ListPromotionsForAdmin(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var sawActive, sawInactive bool
	for _, p := range list {
		if p.Code == "ACTIVE1" && p.IsActive {
			sawActive = true
		}
		if p.Code == "INACTIVE1" && !p.IsActive {
			sawInactive = true
		}
	}
	if !sawActive {
		t.Fatal("active promo missing from admin list")
	}
	if !sawInactive {
		t.Fatal("inactive promo missing from admin list")
	}
}

func TestUpdatePromotion_ChangesFieldsNotCode(t *testing.T) {
	d, repo := openPromoTestDB(t)
	ctx := context.Background()

	// customer_id carries a real FK to customers(id).
	mustExec(t, d, `INSERT INTO customers (id, name) VALUES ('cust-1', 'Targeted Customer')`)

	if err := repo.CreatePromotion(ctx, "EDITME", PromotionInput{
		Type:        "amount",
		Value:       100,
		Description: "original",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := repo.UpdatePromotion(ctx, "EDITME", PromotionInput{
		Type:        "percent",
		Value:       500,
		Description: "updated",
		StartsAt:    "2026-01-01T00:00:00Z",
		EndsAt:      "2026-12-31T00:00:00Z",
		CustomerID:  "cust-1",
	}); err != nil {
		t.Fatalf("update: %v", err)
	}

	list, err := repo.ListPromotionsForAdmin(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, p := range list {
		if p.Code == "EDITME" {
			if p.Type != "percent" || p.Value != 500 || p.Description != "updated" {
				t.Fatalf("edit did not apply: %+v", p)
			}
			if p.StartsAt == "" || p.EndsAt == "" || p.CustomerID != "cust-1" {
				t.Fatalf("edit did not apply dates/customer: %+v", p)
			}
			return
		}
	}
	t.Fatal("EDITME not found after update")
}

func TestUpdatePromotion_UnknownCode(t *testing.T) {
	_, repo := openPromoTestDB(t)
	ctx := context.Background()

	if err := repo.UpdatePromotion(ctx, "GHOST", PromotionInput{Type: "amount", Value: 100}); err == nil {
		t.Fatal("UpdatePromotion(unknown) must error")
	}
}

func TestSetPromotionActive_TogglesAndUnknownErrors(t *testing.T) {
	_, repo := openPromoTestDB(t)
	ctx := context.Background()

	if err := repo.CreatePromotion(ctx, "TOGGLE1", PromotionInput{Type: "amount", Value: 100}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := repo.SetPromotionActive(ctx, "TOGGLE1", false); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	list, _ := repo.ListPromotionsForAdmin(ctx)
	for _, p := range list {
		if p.Code == "TOGGLE1" && p.IsActive {
			t.Fatal("deactivate did not take effect")
		}
	}
	if err := repo.SetPromotionActive(ctx, "TOGGLE1", true); err != nil {
		t.Fatalf("reactivate: %v", err)
	}
	list, _ = repo.ListPromotionsForAdmin(ctx)
	found := false
	for _, p := range list {
		if p.Code == "TOGGLE1" {
			found = true
			if !p.IsActive {
				t.Fatal("reactivate did not take effect")
			}
		}
	}
	if !found {
		t.Fatal("TOGGLE1 missing after reactivate")
	}

	if err := repo.SetPromotionActive(ctx, "GHOST", false); err == nil {
		t.Fatal("SetPromotionActive(unknown) must error")
	}
}

// Regression proof (ut-docs#634 acceptance criterion): a promotion created
// through the new admin CreatePromotion path is immediately redeemable via
// the EXISTING, untouched FindActivePromo lookup used by /api/pos/scan.
func TestCreatePromotion_ImmediatelyRedeemableViaFindActivePromo(t *testing.T) {
	_, repo := openPromoTestDB(t)
	ctx := context.Background()

	if err := repo.CreatePromotion(ctx, "NEWCODE", PromotionInput{
		Type:        "amount",
		Value:       250,
		Description: "£2.50 off",
	}); err != nil {
		t.Fatalf("CreatePromotion: %v", err)
	}

	pType, value, ok := repo.FindActivePromo(ctx, "", "NEWCODE")
	if !ok {
		t.Fatal("promo created via CreatePromotion must be found by FindActivePromo")
	}
	if pType != "amount" || value != 250 {
		t.Fatalf("FindActivePromo returned %q/%d, want amount/250", pType, value)
	}

	// Deactivating it must remove it from checkout redemption too.
	if err := repo.SetPromotionActive(ctx, "NEWCODE", false); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	if _, _, ok := repo.FindActivePromo(ctx, "", "NEWCODE"); ok {
		t.Fatal("deactivated promo must not be found by FindActivePromo")
	}
}
