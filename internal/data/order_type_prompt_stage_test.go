package data

import "testing"

// TestNormalizeOrderTypePromptStage_KnownValuesRoundTrip proves the three
// real values (and case/whitespace variants of them) resolve to themselves.
func TestNormalizeOrderTypePromptStage_KnownValuesRoundTrip(t *testing.T) {
	cases := map[string]string{
		OrderTypePromptStageCartTop:                OrderTypePromptStageCartTop,
		OrderTypePromptStageBeforeSale:             OrderTypePromptStageBeforeSale,
		OrderTypePromptStageAtPay:                  OrderTypePromptStageAtPay,
		" " + OrderTypePromptStageBeforeSale + " ": OrderTypePromptStageBeforeSale,
		"AT_PAY":      OrderTypePromptStageAtPay,
		"Before_Sale": OrderTypePromptStageBeforeSale,
	}
	for in, want := range cases {
		if got := NormalizeOrderTypePromptStage(in); got != want {
			t.Fatalf("NormalizeOrderTypePromptStage(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestNormalizeOrderTypePromptStage_GarbageOrUnsetFallsBackToCartTop proves
// a garbled/unset/unknown value never leaves the till in a state stricter
// or stranger than today's always-visible-toggle behaviour.
func TestNormalizeOrderTypePromptStage_GarbageOrUnsetFallsBackToCartTop(t *testing.T) {
	for _, in := range []string{"", "   ", "sometimes", "at-pay", "cartTop", "before sale"} {
		if got := NormalizeOrderTypePromptStage(in); got != OrderTypePromptStageCartTop {
			t.Fatalf("NormalizeOrderTypePromptStage(%q) = %q, want the safe default %q", in, got, OrderTypePromptStageCartTop)
		}
	}
}
