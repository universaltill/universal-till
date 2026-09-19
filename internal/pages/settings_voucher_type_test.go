package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ADR-0105 (ut-docs#1037): the shop chooses its voucher type ONCE, as a
// setting (SumUp's shipped precedent), never per sale. Same manager-gated,
// elevation-wired shape as allow-negative-inventory; persists BOTH keys in
// one write so a shop can never be left on single_purpose with no rate.
func TestVoucherTypeSettingEndpoint(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

	get := func(key string) string {
		t.Helper()
		v, _, _ := d.Settings.Get(t.Context(), key)
		return v
	}
	if v := get(common.KeyVoucherDefaultType); v != "" {
		t.Fatalf("vouchers.default_type must be unset out of the box (multi-purpose), got %q", v)
	}

	if rec := postForm(mux, "/api/settings/voucher-type", url.Values{"type": {"single_purpose"}, "tax_rate_pct": {"7"}}, &cashUser); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), "elevation-dialog") {
		t.Fatalf("cashier voucher-type = %d body=%s, want 200 with the elevation prompt", rec.Code, rec.Body.String())
	}
	if v := get(common.KeyVoucherDefaultType); v != "" {
		t.Fatalf("an un-elevated cashier POST changed the voucher type to %q", v)
	}

	for _, bad := range []url.Values{
		{"type": {"three_purpose"}, "tax_rate_pct": {"7"}},
		{"type": {"single_purpose"}, "tax_rate_pct": {"-1"}},
		{"type": {"single_purpose"}, "tax_rate_pct": {"NaN"}},
		{"type": {"single_purpose"}, "tax_rate_pct": {"abc"}},
	} {
		if rec := postForm(mux, "/api/settings/voucher-type", bad, &mgrUser); rec.Code != http.StatusBadRequest {
			t.Fatalf("voucher-type %v = %d, want 400", bad, rec.Code)
		}
	}

	if rec := postForm(mux, "/api/settings/voucher-type", url.Values{"type": {"single_purpose"}, "tax_rate_pct": {"7"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("enable single-purpose = %d body=%s, want 204", rec.Code, rec.Body.String())
	}
	if v := get(common.KeyVoucherDefaultType); v != "single_purpose" {
		t.Fatalf("stored %s = %q, want single_purpose", common.KeyVoucherDefaultType, v)
	}
	if v := get(common.KeyVoucherSinglePurposeTaxRateBP); v != "700" {
		t.Fatalf("stored %s = %q, want 700 (7%% in basis points)", common.KeyVoucherSinglePurposeTaxRateBP, v)
	}

	// A decimal percent is accepted the same way the service-charge rate is.
	if rec := postForm(mux, "/api/settings/voucher-type", url.Values{"type": {"single_purpose"}, "tax_rate_pct": {"10.5"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("decimal rate = %d, want 204", rec.Code)
	}
	if v := get(common.KeyVoucherSinglePurposeTaxRateBP); v != "1050" {
		t.Fatalf("stored rate = %q, want 1050", v)
	}

	// Switching to single-purpose WITHOUT a rate falls back to the shop's
	// standard rate — never leaves the shop on single_purpose with no rate.
	std := d.CurrentState().TaxRatePct
	if rec := postForm(mux, "/api/settings/voucher-type", url.Values{"type": {"single_purpose"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("single-purpose with no rate = %d, want 204", rec.Code)
	}
	if v, want := get(common.KeyVoucherSinglePurposeTaxRateBP), itoa(std*100); v != want {
		t.Fatalf("stored rate = %q, want the standard rate %s", v, want)
	}

	// Back to multi-purpose: the type flips, the last rate is kept for the
	// next time the shop switches.
	if rec := postForm(mux, "/api/settings/voucher-type", url.Values{"type": {"multi_purpose"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("back to multi-purpose = %d, want 204", rec.Code)
	}
	if v := get(common.KeyVoucherDefaultType); v != "multi_purpose" {
		t.Fatalf("stored type = %q, want multi_purpose", v)
	}
}

// The control has to actually be on the page, with its explanatory text —
// the exact-match redemption restriction is documented in-app (ADR-0105
// Consequences) so a merchant enabling it understands the constraint.
func TestSettingsPageRendersVoucherTypeControl(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	if err := d.Settings.Set(t.Context(), common.KeyVoucherDefaultType, "single_purpose"); err != nil {
		t.Fatal(err)
	}
	if err := d.Settings.Set(t.Context(), common.KeyVoucherSinglePurposeTaxRateBP, "700"); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/settings", nil)
	req = auth.WithUser(req, mgrUser)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /settings = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`id="settings-vouchers"`,
		`id="voucher-type-select"`,
		`id="voucher-sp-rate"`,
		"/api/settings/voucher-type",
		`value="single_purpose" selected`,
		`value="7"`,
		"Gift voucher type",
		"sole payment",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("Settings page missing %q — the voucher-type setting is unreachable", want)
		}
	}
}
