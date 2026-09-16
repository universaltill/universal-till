package pages

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pos"
)

// TestOrderTypePromptStage_NeverAffectsOrderTypeOrTax is a regression pin
// (ut-docs#2282): sale.order_type_prompt_stage is a pure UI-placement
// setting -- the Architect design is explicit that no server-side code
// outside rendering (index_page.go's data attribute, settings_page.go's own
// form) may ever read it, and SetOrderType/tax resolution must stay
// completely oblivious to it. Table-driven over the three modes: for each
// one, the exact same POST /api/pos/order-type + POST /api/pos/scan
// sequence must produce the exact same basket tax as with no prompt-stage
// setting at all.
func TestOrderTypePromptStage_NeverAffectsOrderTypeOrTax(t *testing.T) {
	baselineTax := func() int64 {
		mux, dp := newPOSTestDeps(t)
		if rec := posPostForm(mux, "/api/pos/order-type", "order_type="+pos.OrderTypeTakeaway); rec.Code != 200 {
			t.Fatalf("baseline set order type: %d", rec.Code)
		}
		if _, err := dp.Engine.Scan("ABC"); err != nil {
			t.Fatalf("baseline scan: %v", err)
		}
		return dp.Engine.Basket().Tax.Minor()
	}()

	for _, stage := range []string{
		"", // unset -- the pre-#2282 database shape
		data.OrderTypePromptStageCartTop,
		data.OrderTypePromptStageBeforeSale,
		data.OrderTypePromptStageAtPay,
	} {
		t.Run("stage="+stage, func(t *testing.T) {
			mux, dp := newPOSTestDeps(t)
			if stage != "" {
				if err := dp.Settings.Set(context.Background(), data.OrderTypePromptStageKey, stage); err != nil {
					t.Fatalf("seed prompt stage: %v", err)
				}
			}
			if rec := posPostForm(mux, "/api/pos/order-type", "order_type="+pos.OrderTypeTakeaway); rec.Code != 200 {
				t.Fatalf("set order type: %d", rec.Code)
			}
			if got := dp.Engine.Basket().OrderType; got != pos.OrderTypeTakeaway {
				t.Fatalf("stage %q: order type = %q, want takeaway", stage, got)
			}
			if _, err := dp.Engine.Scan("ABC"); err != nil {
				t.Fatalf("scan: %v", err)
			}
			if got := dp.Engine.Basket().Tax.Minor(); got != baselineTax {
				t.Fatalf("stage %q: tax = %d, want the baseline %d (prompt-stage setting must never affect tax)", stage, got, baselineTax)
			}

			// And back to dine-in -- still fully reversible regardless of
			// the prompt-stage setting.
			if rec := posPostForm(mux, "/api/pos/order-type", "order_type="); rec.Code != 200 {
				t.Fatalf("reset order type: %d", rec.Code)
			}
			if got := dp.Engine.Basket().OrderType; got != "" {
				t.Fatalf("stage %q: order type after reset = %q, want dine-in (empty)", stage, got)
			}
		})
	}
}
