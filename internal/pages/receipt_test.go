package pages

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pos"
)

// TestRenderReceipt_WorksFromAnyWorkingDirectory guards the sale-completion
// path specifically: renderReceipt used to ParseFiles("web/ui/partials/
// receipt.html") straight off disk, so a real install launched from
// anywhere other than the repo/install root would panic the instant a
// checkout completed — the single most critical moment in a POS. Every
// other test in this file uses chdirRoot(t) (its own comment: "chdir to
// repo root so templates resolve during tests") which was true — required
// — before this fix, and is now a vestigial no-op left in place rather
// than proof of anything. This test deliberately does the opposite.
func TestRenderReceipt_WorksFromAnyWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(orig); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	})

	funcs := map[string]any{
		"money":      func(v int64) string { return fmt.Sprintf("$%.2f", float64(v)/100) },
		"barcodesvg": httpx.BarcodeSVG,
		"bpPercent":  func(bp int64) string { return fmt.Sprintf("%.2f%%", float64(bp)/100.0) },
		"T":          func(key string) string { return key },
	}
	lines := []pos.SaleLineInput{{Name: "Apple", Qty: 1, UnitPrice: 100, TaxRateBasisPoints: 0}}
	payments := []pos.PaymentInput{{MethodID: "cash", Amount: 100, Reference: "REF"}}
	if _, err := renderReceipt(funcs, "123", lines, payments, 100, 0, 100, false, 0, "", 0, nil, false, false, false, false, nil, "My Store", receiptDesign{ShowTax: true, ShowBarcode: true}); err != nil {
		t.Fatalf("renderReceipt from an unrelated CWD: %v", err)
	}
}

// Ensure receipt rendering includes discount and totals.
func TestRenderReceipt_DiscountShown(t *testing.T) {
	chdirRoot(t)
	funcs := map[string]any{
		"money":      func(v int64) string { return fmt.Sprintf("$%.2f", float64(v)/100) },
		"barcodesvg": httpx.BarcodeSVG,
		"bpPercent":  func(bp int64) string { return fmt.Sprintf("%.2f%%", float64(bp)/100.0) },
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
	html, err := renderReceipt(funcs, "123", lines, payments, 100, 0, 90, false, 10, "amount", 10, nil, false, false, false, false, nil, "My Store", receiptDesign{ShowTax: true, ShowBarcode: true})
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

// ut-docs#543: when a payment carries card-present reconciliation data
// (masked PAN + auth code), the receipt shows the standard EC-receipt
// line instead of the generic Reference text -- and must NEVER show
// anything but the already-masked value handed to it (masking happens
// upstream, at CompleteSale's validation boundary, not here).
func TestRenderReceipt_ShowsMaskedPANAndAuthCode(t *testing.T) {
	chdirRoot(t)
	funcs := map[string]any{
		"money":      func(v int64) string { return fmt.Sprintf("$%.2f", float64(v)/100) },
		"barcodesvg": httpx.BarcodeSVG,
		"bpPercent":  func(bp int64) string { return fmt.Sprintf("%.2f%%", float64(bp)/100.0) },
		"T": func(key string) string {
			if key == "receipt.auth_code" {
				return "Auth"
			}
			return key
		},
	}
	lines := []pos.SaleLineInput{{Name: "Coffee", Qty: 1, UnitPrice: 370, TaxRateBasisPoints: 0}}
	payments := []pos.PaymentInput{{
		MethodID: "card", Amount: 370,
		MaskedPAN: "VISA •••• 4242", AuthCode: "013579",
		// Reference is set too, so the test proves MaskedPAN wins the
		// generic-Reference-line fallback rather than both rendering.
		Reference: "should-not-appear",
	}}
	html, err := renderReceipt(funcs, "123", lines, payments, 370, 0, 370, false, 0, "", 0, nil, false, false, false, false, nil, "My Store", receiptDesign{ShowTax: true, ShowBarcode: true})
	if err != nil {
		t.Fatalf("renderReceipt error: %v", err)
	}
	if !strings.Contains(html, "VISA •••• 4242") {
		t.Fatalf("expected masked PAN in receipt html, got: %s", html)
	}
	if !strings.Contains(html, "013579") {
		t.Fatalf("expected auth code in receipt html, got: %s", html)
	}
	if strings.Contains(html, "should-not-appear") {
		t.Fatalf("expected the generic Reference line to be suppressed when MaskedPAN is set, got: %s", html)
	}
}

// Existing (non-card-present) payment methods keep showing today's
// Reference line unchanged -- no MaskedPAN means no behaviour change.
func TestRenderReceipt_NoCardPresentFieldsFallsBackToReference(t *testing.T) {
	chdirRoot(t)
	funcs := map[string]any{
		"money":      func(v int64) string { return fmt.Sprintf("$%.2f", float64(v)/100) },
		"barcodesvg": httpx.BarcodeSVG,
		"bpPercent":  func(bp int64) string { return fmt.Sprintf("%.2f%%", float64(bp)/100.0) },
		"T":          func(key string) string { return key },
	}
	lines := []pos.SaleLineInput{{Name: "Tea", Qty: 1, UnitPrice: 250, TaxRateBasisPoints: 0}}
	payments := []pos.PaymentInput{{MethodID: "cash", Amount: 250, Reference: "sumup-ref-1"}}
	html, err := renderReceipt(funcs, "123", lines, payments, 250, 0, 250, false, 0, "", 0, nil, false, false, false, false, nil, "My Store", receiptDesign{ShowTax: true, ShowBarcode: true})
	if err != nil {
		t.Fatalf("renderReceipt error: %v", err)
	}
	if !strings.Contains(html, "sumup-ref-1") {
		t.Fatalf("expected Reference to still render when no card-present fields are set: %s", html)
	}
}

func TestRenderReceipt_LegalText(t *testing.T) {
	chdirRoot(t)
	funcs := map[string]any{
		"money":      func(v int64) string { return fmt.Sprintf("$%.2f", float64(v)/100) },
		"barcodesvg": httpx.BarcodeSVG,
		"bpPercent":  func(bp int64) string { return fmt.Sprintf("%.2f%%", float64(bp)/100.0) },
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
	html, err := renderReceipt(funcs, "123", lines, payments, 100, 0, 100, false, 0, "", 0, legalBlocks, false, false, false, false, nil, "My Store", receiptDesign{ShowTax: true, ShowBarcode: true})
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
		"bpPercent":  func(bp int64) string { return fmt.Sprintf("%.2f%%", float64(bp)/100.0) },
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
	html, err := renderReceipt(funcs, "123", lines, payments, 100, 0, 100, false, 0, "", 0, nil, false, false, false, false, nil, "My Store", receiptDesign{ShowTax: true, ShowBarcode: true})
	if err != nil {
		t.Fatalf("renderReceipt error: %v", err)
	}
	if strings.Contains(html, `<div class="receipt-legal">`) {
		t.Fatalf("did not expect legal text block in receipt html, got: %s", html)
	}
}

// The on-screen receipt must follow the same owner design as the thermal
// print (docs: receipt-designer.md).
func TestRenderReceiptHonorsDesign(t *testing.T) {
	chdirRoot(t)
	funcs := map[string]any{
		"money":      func(v int64) string { return fmt.Sprintf("$%.2f", float64(v)/100) },
		"barcodesvg": httpx.BarcodeSVG,
		"bpPercent":  func(bp int64) string { return fmt.Sprintf("%.2f%%", float64(bp)/100.0) },
		"T":          func(key string) string { return key },
	}
	lines := []pos.SaleLineInput{
		{Name: "Apple", SKU: "SKU-9", Qty: 1, UnitPrice: 100, TaxRateBasisPoints: 0},
	}
	design := receiptDesign{
		Header:  []string{"12 High Street", "Tel 0123"},
		Footer:  "Thanks for shopping!",
		ShowSKU: true,
		// ShowTax and ShowBarcode off: subtotal/tax rows + barcode hidden.
	}
	html, err := renderReceipt(funcs, "123", lines, nil, 100, 0, 100, false, 0, "", 0, nil, false, false, false, false, nil, "Corner Shop", design)
	if err != nil {
		t.Fatalf("renderReceipt error: %v", err)
	}
	for _, want := range []string{"Corner Shop", "12 High Street", "Thanks for shopping!", "SKU-9"} {
		if !strings.Contains(html, want) {
			t.Fatalf("design element %q missing from receipt html", want)
		}
	}
	if strings.Contains(html, "basket.subtotal") || strings.Contains(html, `class="barcode"`) {
		t.Fatal("hidden design sections still rendered")
	}
}

// ADR-0048: a sale completed during an active TSE-override window carries a
// receipt line marking it — and no such line renders otherwise.
func TestRenderReceipt_UnsignedOverrideLine(t *testing.T) {
	chdirRoot(t)
	funcs := map[string]any{
		"money":      func(v int64) string { return fmt.Sprintf("$%.2f", float64(v)/100) },
		"barcodesvg": httpx.BarcodeSVG,
		"bpPercent":  func(bp int64) string { return fmt.Sprintf("%.2f%%", float64(bp)/100.0) },
		"T":          func(key string) string { return key },
	}
	lines := []pos.SaleLineInput{{Name: "Apple", Qty: 1, UnitPrice: 100, TaxRateBasisPoints: 0}}
	payments := []pos.PaymentInput{{MethodID: "cash", Amount: 100}}

	html, err := renderReceipt(funcs, "123", lines, payments, 100, 0, 100, false, 0, "", 0, nil, false, true, false, false, nil, "My Store", receiptDesign{ShowTax: true})
	if err != nil {
		t.Fatalf("renderReceipt: %v", err)
	}
	if !strings.Contains(html, "receipt.fiscal.unsigned_override") {
		t.Fatalf("expected the unsigned-override marker line, got: %s", html)
	}

	html, err = renderReceipt(funcs, "123", lines, payments, 100, 0, 100, false, 0, "", 0, nil, false, false, false, false, nil, "My Store", receiptDesign{ShowTax: true})
	if err != nil {
		t.Fatalf("renderReceipt: %v", err)
	}
	if strings.Contains(html, "receipt.fiscal.unsigned_override") {
		t.Fatalf("no marker line expected without an active override, got: %s", html)
	}
}

// ADR-0044 proceed-and-declare (ut-docs#675): a sale whose fiscal.sign.ask
// dispatch failed carries a visible outage-notice line — and no such line
// renders otherwise.
func TestRenderReceipt_UnsignedFiscalSigningLine(t *testing.T) {
	chdirRoot(t)
	funcs := map[string]any{
		"money":      func(v int64) string { return fmt.Sprintf("$%.2f", float64(v)/100) },
		"barcodesvg": httpx.BarcodeSVG,
		"bpPercent":  func(bp int64) string { return fmt.Sprintf("%.2f%%", float64(bp)/100.0) },
		"T":          func(key string) string { return key },
	}
	lines := []pos.SaleLineInput{{Name: "Apple", Qty: 1, UnitPrice: 100, TaxRateBasisPoints: 0}}
	payments := []pos.PaymentInput{{MethodID: "cash", Amount: 100}}

	html, err := renderReceipt(funcs, "123", lines, payments, 100, 0, 100, false, 0, "", 0, nil, false, false, true, false, nil, "My Store", receiptDesign{ShowTax: true})
	if err != nil {
		t.Fatalf("renderReceipt: %v", err)
	}
	if !strings.Contains(html, "receipt.fiscal.unsigned_signing") {
		t.Fatalf("expected the fiscal-signing outage line, got: %s", html)
	}

	html, err = renderReceipt(funcs, "123", lines, payments, 100, 0, 100, false, 0, "", 0, nil, false, false, false, false, nil, "My Store", receiptDesign{ShowTax: true})
	if err != nil {
		t.Fatalf("renderReceipt: %v", err)
	}
	if strings.Contains(html, "receipt.fiscal.unsigned_signing") {
		t.Fatalf("no outage line expected for a signed sale, got: %s", html)
	}
}

// ut-docs#835: a sale whose signer declared it CANNOT be signed as
// presented carries its own, differently-worded notice line — and no such
// line renders otherwise. Distinct key from unsigned_signing above: the two
// must never share wording, since one implies a connectivity outage and the
// other explicitly is not one.
func TestRenderReceipt_UnsignedCannotSignLine(t *testing.T) {
	chdirRoot(t)
	funcs := map[string]any{
		"money":      func(v int64) string { return fmt.Sprintf("$%.2f", float64(v)/100) },
		"barcodesvg": httpx.BarcodeSVG,
		"bpPercent":  func(bp int64) string { return fmt.Sprintf("%.2f%%", float64(bp)/100.0) },
		"T":          func(key string) string { return key },
	}
	lines := []pos.SaleLineInput{{Name: "Apple", Qty: 1, UnitPrice: 100, TaxRateBasisPoints: 0}}
	payments := []pos.PaymentInput{{MethodID: "cash", Amount: 100}}

	html, err := renderReceipt(funcs, "123", lines, payments, 100, 0, 100, false, 0, "", 0, nil, false, false, false, true, nil, "My Store", receiptDesign{ShowTax: true})
	if err != nil {
		t.Fatalf("renderReceipt: %v", err)
	}
	if !strings.Contains(html, "receipt.fiscal.unsigned_cannot_sign") {
		t.Fatalf("expected the cannot-sign notice line, got: %s", html)
	}
	if strings.Contains(html, "receipt.fiscal.unsigned_signing") {
		t.Fatalf("cannot-sign must not also render the outage-wording key, got: %s", html)
	}

	html, err = renderReceipt(funcs, "123", lines, payments, 100, 0, 100, false, 0, "", 0, nil, false, false, false, false, nil, "My Store", receiptDesign{ShowTax: true})
	if err != nil {
		t.Fatalf("renderReceipt: %v", err)
	}
	if strings.Contains(html, "receipt.fiscal.unsigned_cannot_sign") {
		t.Fatalf("no cannot-sign line expected for a signed sale, got: %s", html)
	}
}
