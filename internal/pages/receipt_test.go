package pages

import (
	"fmt"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pos"
)

// Ensure receipt rendering includes discount and totals.
func TestRenderReceipt_DiscountShown(t *testing.T) {
	chdirRoot(t)
	funcs := map[string]any{
		"money":      func(v int64) string { return fmt.Sprintf("$%.2f", float64(v)/100) },
		"barcodesvg": httpx.BarcodeSVG,
		"bpPercent": func(bp int64) string { return fmt.Sprintf("%.2f%%", float64(bp)/100.0) },
		"T": func(key string) string {
			switch key {
			case "receipt.legal.plugin_label":
				return "Legal notice (%s v%s)"
			case "receipt.printer.unavailable":
				return "Printer unavailable"
			case "receipt.printer.retry":
				return "Retry print"
			case "basket.discount":
				return "Discount"
			case "basket.total":
				return "Total"
			case "basket.subtotal":
				return "Subtotal"
			case "basket.tax":
				return "Tax"
			case "receipt.payments":
				return "Payments"
			}
			return key
		},
	}
	lines := []pos.SaleLineInput{
		{Name: "Apple", Qty: 1, UnitPrice: 100, TaxRateBasisPoints: 0},
	}
	payments := []pos.PaymentInput{{MethodID: "cash", Amount: 100, ChangeGiven: 10, Reference: "REF"}}
	html, err := renderReceipt(funcs, "123", lines, payments, 100, 0, 90, false, 10, "amount", 10, nil, false)
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
	if !strings.Contains(html, "Payments") || !strings.Contains(html, "cash") {
		t.Fatalf("expected payments breakdown in receipt html: %s", html)
	}
}

func TestRenderReceipt_LegalText(t *testing.T) {
	chdirRoot(t)
	funcs := map[string]any{
		"money":      func(v int64) string { return fmt.Sprintf("$%.2f", float64(v)/100) },
		"barcodesvg": httpx.BarcodeSVG,
		"bpPercent": func(bp int64) string { return fmt.Sprintf("%.2f%%", float64(bp)/100.0) },
		"T": func(key string) string {
			switch key {
			case "receipt.legal.plugin_label":
				return "Legal notice (%s v%s)"
			case "receipt.printer.unavailable":
				return "Printer unavailable"
			case "receipt.printer.retry":
				return "Retry print"
			case "basket.discount":
				return "Discount"
			case "basket.total":
				return "Total"
			case "basket.subtotal":
				return "Subtotal"
			case "basket.tax":
				return "Tax"
			case "receipt.payments":
				return "Payments"
			}
			return key
		},
	}
	lines := []pos.SaleLineInput{
		{Name: "Apple", Qty: 1, UnitPrice: 100, TaxRateBasisPoints: 0},
	}
	payments := []pos.PaymentInput{{MethodID: "cash", Amount: 100, ChangeGiven: 0}}
	legalBlocks := []receiptLegalBlock{
		{
			PluginName:    "TaxPlugin",
			PluginVersion: "1.2.3",
			Lines:         []string{"VAT Reg 123"},
		},
	}
	html, err := renderReceipt(funcs, "123", lines, payments, 100, 0, 100, false, 0, "", 0, legalBlocks, false)
	if err != nil {
		t.Fatalf("renderReceipt error: %v", err)
	}
	if !strings.Contains(html, "VAT Reg 123") {
		t.Fatalf("expected legal text in receipt html, got: %s", html)
	}
	if !strings.Contains(html, "TaxPlugin") || !strings.Contains(html, "1.2.3") {
		t.Fatalf("expected plugin version context in receipt html, got: %s", html)
	}
}

func TestRenderReceipt_NoLegalText(t *testing.T) {
	chdirRoot(t)
	funcs := map[string]any{
		"money":      func(v int64) string { return fmt.Sprintf("$%.2f", float64(v)/100) },
		"barcodesvg": httpx.BarcodeSVG,
		"bpPercent": func(bp int64) string { return fmt.Sprintf("%.2f%%", float64(bp)/100.0) },
		"T": func(key string) string {
			switch key {
			case "receipt.legal.plugin_label":
				return "Legal notice (%s v%s)"
			case "receipt.printer.unavailable":
				return "Printer unavailable"
			case "receipt.printer.retry":
				return "Retry print"
			case "basket.discount":
				return "Discount"
			case "basket.total":
				return "Total"
			case "basket.subtotal":
				return "Subtotal"
			case "basket.tax":
				return "Tax"
			case "receipt.payments":
				return "Payments"
			}
			return key
		},
	}
	lines := []pos.SaleLineInput{
		{Name: "Apple", Qty: 1, UnitPrice: 100, TaxRateBasisPoints: 0},
	}
	payments := []pos.PaymentInput{{MethodID: "cash", Amount: 100, ChangeGiven: 0}}
	html, err := renderReceipt(funcs, "123", lines, payments, 100, 0, 100, false, 0, "", 0, nil, false)
	if err != nil {
		t.Fatalf("renderReceipt error: %v", err)
	}
	if strings.Contains(html, `<div class="receipt-legal">`) {
		t.Fatalf("did not expect legal text block in receipt html, got: %s", html)
	}
}
