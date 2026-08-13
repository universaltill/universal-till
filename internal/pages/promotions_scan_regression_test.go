package pages

// Regression proof for ut-docs#634's acceptance criterion: creating a promo
// code through the new admin CreatePromotion path must not disturb
// /api/pos/scan's existing FindActivePromo redeem behavior in any way.

import (
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

func TestPromotionCreatedViaAdminIsRedeemableAtScan(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	repo := data.NewPOSRepo(dp.Db)

	if err := repo.CreatePromotion(t.Context(), "ADMINPROMO", data.PromotionInput{
		Type:        "amount",
		Value:       150, // £1.50 off in minor units
		Description: "created via admin UI path",
	}); err != nil {
		t.Fatalf("CreatePromotion: %v", err)
	}

	rec := posPostForm(mux, "/api/pos/scan", "code=ADMINPROMO")
	if rec.Code != 200 {
		t.Fatalf("scan admin-created promo: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if dp.Engine.SaleDiscount().Minor() != 150 {
		t.Fatalf("SaleDiscount = %d, want 150", dp.Engine.SaleDiscount().Minor())
	}
	if !strings.Contains(rec.Body.String(), "ADMINPROMO") {
		t.Fatalf("expected promo-applied toast referencing the code, got: %s", rec.Body.String())
	}

	// Deactivating via the admin path removes it from checkout redemption.
	if err := repo.SetPromotionActive(t.Context(), "ADMINPROMO", false); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	dp.Engine.SetDiscount(0) // reset from the earlier scan before re-testing
	rec = posPostForm(mux, "/api/pos/scan", "code=ADMINPROMO")
	if rec.Code != 200 {
		t.Fatalf("scan deactivated promo: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if dp.Engine.SaleDiscount().Minor() != 0 {
		t.Fatalf("deactivated promo must not apply a discount, got %d", dp.Engine.SaleDiscount().Minor())
	}
}
