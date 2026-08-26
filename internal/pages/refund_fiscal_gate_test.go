package pages

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/fiscal"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ADR-0048's German TSE hard gate was wired into completeTender (the
// cashier/kiosk sale path) only — a refund moves real money and is
// aufzeichnungspflichtig under KassenSichV the same as a sale, so it must
// be blocked the same way (ut-docs#731, decided 2026-08-18). These tests
// mirror fiscal_gate_test.go's own sale-path coverage (no-TSE-configured
// block, shadow-mode exemption, override path, non-German regression),
// applied to the /api/refund handler.

// newRefundFiscalTestDeps is newRefundTestDeps (refund_page_test.go) plus
// the fiscal override endpoint, needed to exercise the AllowedWithOverride
// path the same way fiscal_gate_test.go's newFiscalTestDeps does for a sale.
func newRefundFiscalTestDeps(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	mux, dp, _ := newRefundTestDeps(t)
	registerFiscalAPI(mux, dp)
	return mux, dp
}

func postRefundForm(t *testing.T, mux *http.ServeMux, receiptNo, qty string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/refund", strings.NewReader("receipt="+receiptNo+"&qty_0="+qty))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func countReturns(t *testing.T, dp *common.Deps) int {
	t.Helper()
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM sales WHERE sale_type='return'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// German shop, system of record, no TSE configured: a refund must not
// complete — localized refusal, no return row created.
func TestFiscalGate_RefundBlockedWhenTSENeverConfigured(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newRefundFiscalTestDeps(t)
	makeGermanSystemOfRecord(t, dp)
	_, receiptNo := seedCompletedSaleForRefund(t, dp)

	rec := postRefundForm(t, mux, receiptNo, "2")
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	// The localized refund.error.fiscal_never_configured copy specifically
	// -- not just "contains TSE", which the raw, un-localized sentinel
	// error text ("fiscal gate: shop is system of record but no TSE is
	// configured") would ALSO satisfy, and so would never have caught a
	// leak of that raw text to the operator (ut-docs#731 review finding
	// B1).
	if !strings.Contains(rec.Body.String(), "Refund not completed") || !strings.Contains(rec.Body.String(), "technical security device") {
		t.Fatalf("expected the localized refund.error.fiscal_never_configured copy, got: %s", rec.Body.String())
	}
	if got := countReturns(t, dp); got != 0 {
		t.Fatalf("expected no return row, got %d", got)
	}
}

// The refusal copy must come from the locale files, not a Go literal --
// mirrors fiscal_gate_test.go's own TestFiscalGate_RefusalIsTranslated for
// the sale path.
func TestFiscalGate_RefundRefusalIsTranslated(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newRefundFiscalTestDeps(t)
	makeGermanSystemOfRecord(t, dp)
	_, receiptNo := seedCompletedSaleForRefund(t, dp)

	raw, err := os.ReadFile(filepath.Join("web", "locales", "fa.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fa map[string]string
	if err := json.Unmarshal(raw, &fa); err != nil {
		t.Fatal(err)
	}
	want := fa["refund.error.fiscal_never_configured"]
	if want == "" {
		t.Fatal("fa.json is missing refund.error.fiscal_never_configured")
	}

	req := httptest.NewRequest(http.MethodPost, "/api/refund?lang=fa",
		strings.NewReader("receipt="+receiptNo+"&qty_0=2"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), want) {
		t.Fatalf("expected fa translation %q in refusal, got: %s", want, rec.Body.String())
	}
}

// Same shop in shadow/demo mode (system_of_record unset): the refund
// completes normally regardless of TSE state.
func TestFiscalGate_RefundShadowModeCompletesNormally(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newRefundFiscalTestDeps(t)
	dp.UpdateState(func(s *common.RuntimeState) { s.Country = "DE" })
	_, receiptNo := seedCompletedSaleForRefund(t, dp)

	rec := postRefundForm(t, mux, receiptNo, "2")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := countReturns(t, dp); got != 1 {
		t.Fatalf("expected 1 return, got %d", got)
	}
}

// Non-German shop is completely unaffected, even with every fiscal key set —
// the regression pin for the existing refund flow.
func TestFiscalGate_RefundNonGermanShopUnaffected(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newRefundFiscalTestDeps(t)
	for k, v := range map[string]string{
		fiscal.KeySystemOfRecord:  "true",
		fiscal.KeyTSEConfigured:   "false",
		fiscal.KeyTSEFailingSince: "2026-08-14T09:00:00Z",
	} {
		if err := dp.Settings.Set(t.Context(), k, v); err != nil {
			t.Fatal(err)
		}
	}
	_, receiptNo := seedCompletedSaleForRefund(t, dp)

	rec := postRefundForm(t, mux, receiptNo, "2")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for a non-gated shop, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := countReturns(t, dp); got != 1 {
		t.Fatalf("expected 1 return, got %d", got)
	}
}

// TSE configured but failing, no override: blocked with its own message.
func TestFiscalGate_RefundFailingTSEBlockedWithoutOverride(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newRefundFiscalTestDeps(t)
	seedFailingConfiguredTSE(t, dp)
	_, receiptNo := seedCompletedSaleForRefund(t, dp)

	rec := postRefundForm(t, mux, receiptNo, "2")
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	// Distinguishes this from the never-configured copy above (ut-docs#731
	// review finding S4: the two locale keys are swappable and CI would
	// stay green either way without a message assertion here).
	if !strings.Contains(rec.Body.String(), "currently reported as failing") {
		t.Fatalf("expected the localized refund.error.fiscal_tse_failing copy, got: %s", rec.Body.String())
	}
	if got := countReturns(t, dp); got != 0 {
		t.Fatalf("expected no return row, got %d", got)
	}
}

// The refund screen carries the same persistent override-active banner as
// the sale screen (fiscal_kiosk_banner_test.go's
// TestFiscalGate_SaleScreenBannerDuringOverride) -- and none outside one.
// Before this (ut-docs#1001), a cashier processing a refund under an
// active override got no warning until they submitted the form.
func TestFiscalGate_RefundScreenBannerDuringOverride(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newRefundFiscalTestDeps(t)
	seedFailingConfiguredTSE(t, dp)
	_, receiptNo := seedCompletedSaleForRefund(t, dp)

	get := func() string {
		req := httptest.NewRequest(http.MethodGet, "/refund/"+receiptNo, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /refund/%s failed: %d %s", receiptNo, rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}

	// Blocked (no override): no banner -- the refusal itself rides the
	// submit attempt, not the page.
	if body := get(); strings.Contains(body, "fiscal-override-banner") {
		t.Fatalf("no banner expected without an active override")
	}

	// Active override: banner present.
	req := httptest.NewRequest(http.MethodPost, "/api/fiscal/tse-override", strings.NewReader(validOverrideBody("")))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req = auth.WithUser(req, auth.User{ID: "user1", Role: "admin"})
	grantRec := httptest.NewRecorder()
	mux.ServeHTTP(grantRec, req)
	if grantRec.Code != http.StatusOK {
		t.Fatalf("expected 200 for an admin grant, got %d: %s", grantRec.Code, grantRec.Body.String())
	}
	if body := get(); !strings.Contains(body, "fiscal-override-banner") {
		t.Fatalf("expected the override banner on the refund screen, got: %s", body)
	}
}

// An active admin override unblocks the refund and writes the same
// per-completion unsigned_override audit marker completeTender writes for a
// sale, attached to the RETURN sale row (not the original sale).
func TestFiscalGate_RefundOverrideUnblocksAndAudits(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newRefundFiscalTestDeps(t)
	seedFailingConfiguredTSE(t, dp)
	_, receiptNo := seedCompletedSaleForRefund(t, dp)

	// Blocked before the grant.
	if rec := postRefundForm(t, mux, receiptNo, "2"); rec.Code != http.StatusConflict {
		t.Fatalf("expected the pre-override refund to be refused, got %d: %s", rec.Code, rec.Body.String())
	}

	req := httptest.NewRequest(http.MethodPost, "/api/fiscal/tse-override", strings.NewReader(validOverrideBody("")))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req = auth.WithUser(req, auth.User{ID: "user1", Role: "admin"})
	grantRec := httptest.NewRecorder()
	mux.ServeHTTP(grantRec, req)
	if grantRec.Code != http.StatusOK {
		t.Fatalf("expected 200 for an admin grant, got %d: %s", grantRec.Code, grantRec.Body.String())
	}

	rec := postRefundForm(t, mux, receiptNo, "2")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 under an active override, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := countReturns(t, dp); got != 1 {
		t.Fatalf("expected 1 return under an active override, got %d", got)
	}

	var returnID, payload string
	if err := dp.Db.QueryRow(`SELECT entity_id, data_json FROM audit_log WHERE entity_type='sale' AND action='unsigned_override'`).
		Scan(&returnID, &payload); err != nil {
		t.Fatalf("expected a sale/unsigned_override audit row: %v", err)
	}
	var realReturnID string
	if err := dp.Db.QueryRow(`SELECT id FROM sales WHERE sale_type='return'`).Scan(&realReturnID); err != nil {
		t.Fatal(err)
	}
	if returnID != realReturnID {
		t.Fatalf("unsigned_override marker not attached to the return: %q != %q", returnID, realReturnID)
	}
	if !strings.Contains(payload, "user1") {
		t.Fatalf("per-return marker missing actor: %s", payload)
	}
}
