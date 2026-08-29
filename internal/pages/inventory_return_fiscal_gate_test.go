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

// CreateReturn (/api/inventory/return) is the SECOND refund/return call
// site found bypassing ADR-0048's German TSE hard gate while scoping
// ut-docs#731 — the ticket named refund_page.go's postRefund handler, but
// this handler calls pos.CompleteSale directly too, from the inventory
// page's own return form (web/ui/pages/inventory.html). Same coverage as
// refund_fiscal_gate_test.go, applied here.

func newInventoryFiscalTestDeps(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	mux, dp := newInventoryAPITestDeps(t)
	registerFiscalAPI(mux, dp)
	return mux, dp
}

func countInventoryReturns(t *testing.T, dp *common.Deps) int {
	t.Helper()
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM sales WHERE sale_type='return'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// German shop, system of record, no TSE configured: CreateReturn must not
// complete either.
func TestFiscalGate_CreateReturnBlockedWhenTSENeverConfigured(t *testing.T) {
	mux, dp := newInventoryFiscalTestDeps(t)
	makeGermanSystemOfRecord(t, dp)
	saleID, _, lineID := seedCompletedSaleForReturn(t, dp)

	rec := postInvJSON(t, mux, "/api/inventory/return",
		`{"original_sale_id":"`+saleID+`","reason":"faulty","lines":[{"line_id":"`+lineID+`","quantity":1}]}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	// The localized refund.error.fiscal_never_configured copy specifically
	// -- not just "contains fiscal-signing device", which the raw,
	// un-localized sentinel error text ("fiscal gate: shop is system of
	// record but no fiscal-signing device is configured") would ALSO
	// satisfy. The "Refund not completed" prefix is what actually
	// distinguishes the two (the raw sentinel never carries it), and so
	// would still have caught this handler leaking that raw text to the
	// operator (ut-docs#731 review finding B1 -- this call site is exactly
	// where it happened).
	if !strings.Contains(rec.Body.String(), "Refund not completed") || !strings.Contains(rec.Body.String(), "fiscal-signing device") {
		t.Fatalf("expected the localized refund.error.fiscal_never_configured copy, got: %s", rec.Body.String())
	}
	if got := countInventoryReturns(t, dp); got != 0 {
		t.Fatalf("expected no return row, got %d", got)
	}
}

// The refusal copy must come from the locale files, not raw Go/SQL error
// text -- mirrors fiscal_gate_test.go's own TestFiscalGate_RefusalIsTranslated
// for the sale path, applied to CreateReturn.
func TestFiscalGate_CreateReturnRefusalIsTranslated(t *testing.T) {
	mux, dp := newInventoryFiscalTestDeps(t)
	makeGermanSystemOfRecord(t, dp)
	saleID, _, lineID := seedCompletedSaleForReturn(t, dp)

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

	req := httptest.NewRequest(http.MethodPost, "/api/inventory/return?lang=fa",
		strings.NewReader(`{"original_sale_id":"`+saleID+`","reason":"faulty","lines":[{"line_id":"`+lineID+`","quantity":1}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), want) {
		t.Fatalf("expected fa translation %q in refusal, got: %s", want, rec.Body.String())
	}
}

// Shadow/demo mode: unaffected.
func TestFiscalGate_CreateReturnShadowModeCompletesNormally(t *testing.T) {
	mux, dp := newInventoryFiscalTestDeps(t)
	dp.UpdateState(func(s *common.RuntimeState) { s.Country = "DE" })
	saleID, _, lineID := seedCompletedSaleForReturn(t, dp)

	rec := postInvJSON(t, mux, "/api/inventory/return",
		`{"original_sale_id":"`+saleID+`","reason":"faulty","lines":[{"line_id":"`+lineID+`","quantity":1}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := countInventoryReturns(t, dp); got != 1 {
		t.Fatalf("expected 1 return, got %d", got)
	}
}

// Non-German shop: regression pin, unaffected even with every fiscal key
// set to the most blocking combination.
func TestFiscalGate_CreateReturnNonGermanShopUnaffected(t *testing.T) {
	mux, dp := newInventoryFiscalTestDeps(t)
	for k, v := range map[string]string{
		fiscal.KeySystemOfRecord:  "true",
		fiscal.KeyTSEConfigured:   "false",
		fiscal.KeyTSEFailingSince: "2026-08-14T09:00:00Z",
	} {
		if err := dp.Settings.Set(t.Context(), k, v); err != nil {
			t.Fatal(err)
		}
	}
	saleID, _, lineID := seedCompletedSaleForReturn(t, dp)

	rec := postInvJSON(t, mux, "/api/inventory/return",
		`{"original_sale_id":"`+saleID+`","reason":"faulty","lines":[{"line_id":"`+lineID+`","quantity":1}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for a non-gated shop, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := countInventoryReturns(t, dp); got != 1 {
		t.Fatalf("expected 1 return, got %d", got)
	}
}

// TSE configured but failing, no override: blocked.
func TestFiscalGate_CreateReturnFailingTSEBlockedWithoutOverride(t *testing.T) {
	mux, dp := newInventoryFiscalTestDeps(t)
	seedFailingConfiguredTSE(t, dp)
	saleID, _, lineID := seedCompletedSaleForReturn(t, dp)

	rec := postInvJSON(t, mux, "/api/inventory/return",
		`{"original_sale_id":"`+saleID+`","reason":"faulty","lines":[{"line_id":"`+lineID+`","quantity":1}]}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	// Distinguishes this from the never-configured copy above (ut-docs#731
	// review finding S4: the two locale keys are swappable and CI would
	// stay green either way without a message assertion here).
	if !strings.Contains(rec.Body.String(), "currently reported as failing") {
		t.Fatalf("expected the localized refund.error.fiscal_tse_failing copy, got: %s", rec.Body.String())
	}
	if got := countInventoryReturns(t, dp); got != 0 {
		t.Fatalf("expected no return row, got %d", got)
	}
}

// An active admin override unblocks CreateReturn too, and writes the same
// per-completion unsigned_override audit marker on the return sale row.
func TestFiscalGate_CreateReturnOverrideUnblocksAndAudits(t *testing.T) {
	mux, dp := newInventoryFiscalTestDeps(t)
	seedFailingConfiguredTSE(t, dp)
	saleID, _, lineID := seedCompletedSaleForReturn(t, dp)

	returnBody := `{"original_sale_id":"` + saleID + `","reason":"faulty","lines":[{"line_id":"` + lineID + `","quantity":1}]}`

	if rec := postInvJSON(t, mux, "/api/inventory/return", returnBody); rec.Code != http.StatusConflict {
		t.Fatalf("expected the pre-override return to be refused, got %d: %s", rec.Code, rec.Body.String())
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

	rec := postInvJSON(t, mux, "/api/inventory/return", returnBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 under an active override, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := countInventoryReturns(t, dp); got != 1 {
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
