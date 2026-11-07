package pages

import (
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#5743: the on-screen receipt must show the service-charge row the
// printed receipt already has, in the printed order: Subtotal, Discount,
// Tax, Service charge, Vouchers issued, Total.

func renderServiceChargeTotals(t *testing.T, design receiptDesign, discount, serviceCharge int64) string {
	t.Helper()
	chdirRoot(t)
	funcs := voucherTotalsFuncs()
	payments := []pos.PaymentInput{{MethodID: "cash", Amount: 1500}}
	issued := []receiptVoucherIssue{{Code: "GS-1", Amount: 200}}
	html, err := renderReceipt(funcs, "123", nil, payments, 1000, 100, 1500, false, discount, "", 0, nil, false, false, false, false, nil, nil, "My Store", design, "", issued, "", nil, nil, "", "en", serviceCharge)
	if err != nil {
		t.Fatal(err)
	}
	i := strings.Index(html, `class="totals"`)
	if i < 0 {
		t.Fatalf("no totals block: %s", html)
	}
	html = html[i:]
	if j := strings.Index(html, "receipt-payments"); j > 0 {
		html = html[:j]
	}
	return html
}

func TestRenderReceipt_ServiceChargeRowOrdered_5743(t *testing.T) {
	for _, showTax := range []bool{true, false} {
		html := renderServiceChargeTotals(t, receiptDesign{ShowTax: showTax}, 50, 250)
		if !strings.Contains(html, `data-testid="receipt-service-charge"`) {
			t.Fatalf("ShowTax=%v: no service-charge row: %s", showTax, html)
		}
		sc := strings.Index(html, "journal.detail.service_charge")
		if sc < 0 || !strings.Contains(html[sc:], ": $2.50") {
			t.Fatalf("ShowTax=%v: want service charge $2.50: %s", showTax, html)
		}
		total := strings.Index(html, "basket.total")
		if total < sc {
			t.Fatalf("ShowTax=%v: service charge must precede Total: %s", showTax, html)
		}
		disc := strings.Index(html, "basket.discount")
		if disc < 0 || disc > sc {
			t.Fatalf("ShowTax=%v: Discount must precede service charge: %s", showTax, html)
		}
		if showTax {
			sub, tax := strings.Index(html, "basket.subtotal"), strings.Index(html, "basket.tax")
			if !(sub < disc && disc < tax && tax < sc) {
				t.Fatalf("want Subtotal, Discount, Tax, Service charge order: %s", html)
			}
			if v := strings.Index(html, "receipt.vouchers.issued"); v < sc || v > total {
				t.Fatalf("Vouchers issued must sit between service charge and Total: %s", html)
			}
		}
	}
}

func TestRenderReceipt_NoServiceChargeRowWhenZero_5743(t *testing.T) {
	html := renderServiceChargeTotals(t, receiptDesign{ShowTax: true}, 0, 0)
	if strings.Contains(html, "receipt-service-charge") || strings.Contains(html, "journal.detail.service_charge") {
		t.Fatalf("zero service charge must render no row: %s", html)
	}
}
