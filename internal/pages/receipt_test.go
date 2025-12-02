package pages

import (
	"fmt"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/pos"
)

// Ensure receipt rendering includes discount and totals.
func TestRenderReceipt_DiscountShown(t *testing.T) {
	chdirRoot(t)
	funcs := map[string]any{
		"money": func(v int64) string { return fmt.Sprintf("$%.2f", float64(v)/100) },
	}
	lines := []pos.SaleLineInput{
		{Name: "Apple", Qty: 1, UnitPrice: 100, TaxRateBasisPoints: 0},
	}
	html, err := renderReceipt(funcs, "123", lines, 100, 0, 90, false, 10)
	if err != nil {
		t.Fatalf("renderReceipt error: %v", err)
	}
	if !strings.Contains(html, "Discount") {
		t.Fatalf("expected discount in receipt html, got: %s", html)
	}
	if !strings.Contains(html, "$0.10") {
		t.Fatalf("expected discount amount in receipt html, got: %s", html)
	}
	if !strings.Contains(html, "Total") {
		t.Fatalf("expected total in receipt html")
	}
}
