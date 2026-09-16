package pages

import (
	"context"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#2282: the sale screen's <body> carries the resolved
// sale.order_type_prompt_stage setting as a data attribute (base.html) --
// the client-side gating script (index.html) and app.css's at_pay
// toggle-hiding rule both key off this exact attribute, so it must be
// present and correctly resolved on every "/" render.

// Unset (new install) resolves to cart_top -- today's behaviour, and the
// attribute is present rather than silently omitted (an absent attribute
// would also read as "cart_top" client-side, but an explicit value here
// pins the server-side resolution itself, independent of the JS default).
func TestIndexPage_OrderTypePromptStageDefaultsToCartTop(t *testing.T) {
	mux, _ := quickPayTestMux(t)
	home := getHome(t, mux)
	if !strings.Contains(home, `data-order-type-prompt-stage="cart_top"`) {
		t.Fatalf("expected the default cart_top stage on body, got:\n%s", home)
	}
}

// A stored before_sale/at_pay value is reflected verbatim.
func TestIndexPage_OrderTypePromptStageReflectsStoredValue(t *testing.T) {
	for _, stage := range []string{data.OrderTypePromptStageBeforeSale, data.OrderTypePromptStageAtPay} {
		t.Run(stage, func(t *testing.T) {
			mux, dp := quickPayTestMux(t)
			if err := dp.Settings.Set(context.Background(), data.OrderTypePromptStageKey, stage); err != nil {
				t.Fatalf("seed: %v", err)
			}
			home := getHome(t, mux)
			if !strings.Contains(home, `data-order-type-prompt-stage="`+stage+`"`) {
				t.Fatalf("expected stage %q on body, got:\n%s", stage, home)
			}
		})
	}
}

// A garbled stored value falls back to cart_top, same as every other
// unknown-value fallback in this feature (NormalizeOrderTypePromptStage).
func TestIndexPage_OrderTypePromptStageGarbageFallsBackToCartTop(t *testing.T) {
	mux, dp := quickPayTestMux(t)
	if err := dp.Settings.Set(context.Background(), data.OrderTypePromptStageKey, "sometimes"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	home := getHome(t, mux)
	if !strings.Contains(home, `data-order-type-prompt-stage="cart_top"`) {
		t.Fatalf("expected garbage to fall back to cart_top, got:\n%s", home)
	}
}
