package pages

// ut-docs#3560: the sale screen's No Sale action. Rendered-HTML assertions
// against the real handler + template (same fixture as index_quickpay_test.go).
// The click flow is e2e/tests/sale-no-sale-3560.spec.ts.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIndexPage_RendersNoSaleButtons(t *testing.T) {
	mux, _ := quickPayTestMux(t)
	home := getHome(t, mux)

	// The action-row copy: icon-only, labelled, LAST in the row (after Open orders).
	tender := tenderSection(t, home)
	footer := tender[strings.Index(tender, `class="tender-default-footer"`):]
	row := buttonHTML(t, footer, `data-testid="no-sale"`)
	for _, want := range []string{"data-ut-no-sale", `title="No Sale: open the cash drawer without a sale"`, `class="visually-hidden">No Sale<`, "<svg"} {
		if !strings.Contains(row, want) {
			t.Errorf("row button missing %q:\n%s", want, row)
		}
	}
	if strings.Index(footer, `data-testid="no-sale"`) < strings.Index(footer, `data-testid="parked-orders-open"`) {
		t.Errorf("No Sale must come after Open orders in DOM order")
	}

	// The phone copy: visible label, own testid.
	phone := buttonHTML(t, home, `data-testid="no-sale-phone"`)
	if !strings.Contains(phone, "data-ut-no-sale") || !strings.Contains(phone, "No Sale") || !strings.Contains(phone, "compact") {
		t.Errorf("phone copy wrong:\n%s", phone)
	}

	// The status slot carries every message as data-*, and sits outside #basket.
	status := openTag(t, home, `id="no-sale-status"`)
	for _, attr := range []string{"data-msg-opened=", "data-msg-no-printer=", "data-msg-unsupported=", "data-msg-kick-failed=", "data-msg-not-recorded=", "data-msg-failed="} {
		if !strings.Contains(status, attr) {
			t.Errorf("#no-sale-status missing %s: %s", attr, status)
		}
	}
	if !strings.Contains(status, `role="status"`) {
		t.Errorf("#no-sale-status must be role=status: %s", status)
	}
}

// The customer-facing self-order pages never carry the control (R7).
func TestSelfOrderTemplates_NoNoSaleControl(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("..", "..", "web", "ui", "pages", "self_order*.html"))
	more, _ := filepath.Glob(filepath.Join("..", "..", "web", "ui", "partials", "self_order*.html"))
	files = append(files, more...)
	if len(files) == 0 {
		t.Skip("no self-order templates found")
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "data-ut-no-sale") {
			t.Errorf("%s must not render the No Sale control", f)
		}
	}
}
