package pages

// ut-docs#1832: vouchers get a cashier UI. A voucher-type payment method
// (the new built-in 'voucher' row from 013_voucher_payment_method.sql, and
// the unchanged legacy 'gift' row) can never work as a one-tap full-amount
// button: pos.CompleteSale needs a voucher_id on the payment, and the Pay
// grid has no field for one. So the Go side hands the template two
// differently-filtered views of ListActivePaymentMethods:
//
//   - .payMethods / .defaultPayMethod (the Pay grid + the ⚡ quick-pay
//     button) EXCLUDE every Type=="voucher" method;
//   - .paymentMethods (the Split tab's <select>) keeps the FULL list —
//     that's exactly where Voucher (and Gift Card) must appear, next to
//     the voucher-id field app.js reveals for it.
//
// Rendered-HTML assertions, same fixture as index_quickpay_test.go: the
// pages test DB runs the real migration set, so 'gift' and 'voucher' are
// both present without any per-test seeding.

import (
	"strings"
	"testing"
)

// payGridSnippet isolates the overlay's Pay-grid markup.
func payGridSnippet(t *testing.T, home string) string {
	t.Helper()
	start := strings.Index(home, `class="pay-grid"`)
	if start < 0 {
		t.Fatalf("pay-grid missing from home page")
	}
	end := strings.Index(home[start:], "</div>")
	if end < 0 {
		t.Fatalf("pay-grid closing tag not found")
	}
	return home[start : start+end]
}

// splitMethodSelectSnippet isolates the Split tab's method <select>.
func splitMethodSelectSnippet(t *testing.T, home string) string {
	t.Helper()
	start := strings.Index(home, `id="split-tender-method"`)
	if start < 0 {
		t.Fatalf("#split-tender-method missing from home page")
	}
	end := strings.Index(home[start:], "</select>")
	if end < 0 {
		t.Fatalf("#split-tender-method closing tag not found")
	}
	return home[start : start+end]
}

func TestIndexVoucherMethods_PayGridExcludesVoucherTypes(t *testing.T) {
	mux, _ := quickPayTestMux(t)
	home := getHome(t, mux)

	grid := payGridSnippet(t, home)
	for _, id := range []string{"voucher", "gift"} {
		if strings.Contains(grid, `data-method="`+id+`"`) {
			t.Errorf("Pay grid must not offer the voucher-type method %q as a one-tap button:\n%s", id, grid)
		}
	}
	if !strings.Contains(grid, `data-method="card"`) || !strings.Contains(grid, `data-method="cash"`) {
		t.Errorf("Pay grid lost a non-voucher method (cash/card must still render):\n%s", grid)
	}

	sel := splitMethodSelectSnippet(t, home)
	for _, id := range []string{"voucher", "gift", "cash", "card"} {
		if !strings.Contains(sel, `<option value="`+id+`"`) {
			t.Errorf("Split select must list %q (voucher-type methods included):\n%s", id, sel)
		}
	}
}

// Even when the shop's preferred method is set to the voucher method, the
// quick-pay button and the Pay grid's head must fall back to a real one-tap
// method — a voucher can't tender "everything owed" without an id.
func TestIndexVoucherMethods_DefaultNeverVoucher(t *testing.T) {
	mux, dp := quickPayTestMux(t)
	if err := dp.Settings.Set(t.Context(), "payments.default_method", "voucher"); err != nil {
		t.Fatalf("set payments.default_method: %v", err)
	}
	home := getHome(t, mux)

	btn := quickPayButtonSnippet(t, home)
	if strings.Contains(btn, `data-method="voucher"`) || strings.Contains(btn, `data-method="gift"`) {
		t.Fatalf("quick-pay must never tender a voucher-type method: %s", btn)
	}
	grid := payGridSnippet(t, home)
	if strings.Contains(grid, `data-method="voucher"`) {
		t.Fatalf("preferred-method reorder leaked the voucher method into the Pay grid:\n%s", grid)
	}
	sel := splitMethodSelectSnippet(t, home)
	if strings.Contains(sel, `data-default="voucher"`) {
		t.Fatalf("Split select's default must not be the voucher method (its voucher-id field is hidden until chosen): %s", sel)
	}
	if !strings.Contains(sel, `<option value="voucher"`) {
		t.Fatalf("Split select still has to offer the voucher method: %s", sel)
	}
}

// ut-docs#1832 review: a PLUGIN-provided payment method's type is lifted
// verbatim out of its manifest (SyncPluginPaymentMethods reads config_json's
// `method_type` with no canonicalization), so the Pay-grid exclusion has to
// be case-folded — a manifest declaring "Voucher" must not buy a one-tap
// full-amount button that records an untracked tender debiting no voucher.
func TestIndexVoucherMethods_PayGridExcludesMixedCaseVoucherType(t *testing.T) {
	mux, dp := quickPayTestMux(t)
	if _, err := dp.Db.Exec(
		`INSERT INTO payment_methods (id, name, type, is_active, sort_order, plugin_id)
		 VALUES ('plugin_gs', 'Plugin Gutschein', 'Voucher', 1, 50, 'com.t.gs')`); err != nil {
		t.Fatalf("seed mixed-case voucher method: %v", err)
	}
	home := getHome(t, mux)

	grid := payGridSnippet(t, home)
	if strings.Contains(grid, `data-method="plugin_gs"`) {
		t.Errorf("Pay grid must exclude a Type=%q method case-insensitively:\n%s", "Voucher", grid)
	}
	if sel := splitMethodSelectSnippet(t, home); !strings.Contains(sel, `<option value="plugin_gs"`) {
		t.Errorf("Split select must still offer the plugin voucher method: %s", sel)
	}
}
