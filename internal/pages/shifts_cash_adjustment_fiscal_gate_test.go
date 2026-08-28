package pages

import (
	"context"
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

// RecordCashAdjustment (/api/shifts/adjustment, type=payout/adjustment with
// a negative amount) and PfandRueckgabe (/api/shifts/pfandrueckgabe) both
// take cash physically out of the till drawer, the same aufzeichnungspflichtig
// event under KassenSichV a refund/return is — but neither calls
// pos.CompleteSale, so ut-docs#731's sweep of every CompleteSale call site
// never found them. Coverage here mirrors refund_fiscal_gate_test.go and
// inventory_return_fiscal_gate_test.go, applied to these two handlers
// (ut-docs#998).

func newShiftsFiscalTestDeps(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	mux, dp := newShiftsAPITestDeps(t)
	registerFiscalAPI(mux, dp)
	return mux, dp
}

// openShiftForFiscalGateTest opens a shift on register "reg1" and returns
// its shift ID, with auth disabled so the gate under test is isolated from
// the manager-PIN gate.
func openShiftForFiscalGateTest(t *testing.T, mux *http.ServeMux, dp *common.Deps) string {
	t.Helper()
	ctx := context.Background()
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO registers(id,name,is_active) VALUES('reg1','Front Till',1)`); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/shifts/open",
		strings.NewReader(`{"register_id":"reg1","cashier_id":"user1","opening_cash":5000}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("open shift: %d: %s", rec.Code, rec.Body.String())
	}
	var shiftID string
	if err := dp.Db.QueryRowContext(ctx, `SELECT id FROM shifts LIMIT 1`).Scan(&shiftID); err != nil {
		t.Fatal(err)
	}
	return shiftID
}

func countShiftAdjustments(t *testing.T, dp *common.Deps) int {
	t.Helper()
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE entity_type='shift' AND action='cash_adjustment'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func adjustmentRequest(t *testing.T, mux *http.ServeMux, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req = auth.WithUser(req, auth.User{ID: "user1"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// German shop, system of record, no TSE configured: a payout via the
// generic adjustment endpoint must not complete.
func TestFiscalGate_CashAdjustmentPayoutBlockedWhenTSENeverConfigured(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newShiftsFiscalTestDeps(t)
	makeGermanSystemOfRecord(t, dp)
	shiftID := openShiftForFiscalGateTest(t, mux, dp)

	rec := adjustmentRequest(t, mux, "/api/shifts/adjustment",
		`{"shift_id":"`+shiftID+`","type":"payout","amount":-500,"reason":"float top-up to safe"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	// The localized copy specifically, not just any 409 — same finding
	// class as ut-docs#731's B1 (raw sentinel text leaking to the operator).
	if !strings.Contains(rec.Body.String(), "no TSE") && !strings.Contains(rec.Body.String(), "technical security device") {
		t.Fatalf("expected the localized refund.error.fiscal_never_configured copy, got: %s", rec.Body.String())
	}
	if got := countShiftAdjustments(t, dp); got != 0 {
		t.Fatalf("expected no cash_adjustment audit row, got %d", got)
	}
}

// A positive adjustment (cash going IN) is not the payout this gate exists
// for, and must be unaffected even for a German shop with no TSE.
func TestFiscalGate_CashAdjustmentPositiveAmountUnaffected(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newShiftsFiscalTestDeps(t)
	makeGermanSystemOfRecord(t, dp)
	shiftID := openShiftForFiscalGateTest(t, mux, dp)

	rec := adjustmentRequest(t, mux, "/api/shifts/adjustment",
		`{"shift_id":"`+shiftID+`","type":"adjustment","amount":200,"reason":"float top-up"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for a positive adjustment even ungated, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TSE configured but failing, no override: blocked, with the distinct
// "failing" copy (not "never configured" — ut-docs#731 review finding S4).
func TestFiscalGate_CashAdjustmentPayoutFailingTSEBlockedWithoutOverride(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newShiftsFiscalTestDeps(t)
	seedFailingConfiguredTSE(t, dp)
	shiftID := openShiftForFiscalGateTest(t, mux, dp)

	rec := adjustmentRequest(t, mux, "/api/shifts/adjustment",
		`{"shift_id":"`+shiftID+`","type":"payout","amount":-500,"reason":"float top-up to safe"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "currently reported as failing") {
		t.Fatalf("expected the localized refund.error.fiscal_tse_failing copy, got: %s", rec.Body.String())
	}
	if got := countShiftAdjustments(t, dp); got != 0 {
		t.Fatalf("expected no cash_adjustment audit row, got %d", got)
	}
}

// Non-German shop: regression pin, unaffected even with every fiscal key
// set to the most blocking combination.
func TestFiscalGate_CashAdjustmentNonGermanShopUnaffected(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newShiftsFiscalTestDeps(t)
	shiftID := openShiftForFiscalGateTest(t, mux, dp)
	for k, v := range map[string]string{
		fiscal.KeySystemOfRecord:  "true",
		fiscal.KeyTSEConfigured:   "false",
		fiscal.KeyTSEFailingSince: "2026-08-14T09:00:00Z",
	} {
		if err := dp.Settings.Set(t.Context(), k, v); err != nil {
			t.Fatal(err)
		}
	}

	rec := adjustmentRequest(t, mux, "/api/shifts/adjustment",
		`{"shift_id":"`+shiftID+`","type":"payout","amount":-500,"reason":"float top-up to safe"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for a non-gated shop, got %d: %s", rec.Code, rec.Body.String())
	}
}

// An active admin override unblocks the payout, and writes the
// unsigned_override audit marker keyed on the adjustment.
func TestFiscalGate_CashAdjustmentPayoutOverrideUnblocksAndAudits(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newShiftsFiscalTestDeps(t)
	seedFailingConfiguredTSE(t, dp)
	shiftID := openShiftForFiscalGateTest(t, mux, dp)
	body := `{"shift_id":"` + shiftID + `","type":"payout","amount":-500,"reason":"float top-up to safe"}`

	if rec := adjustmentRequest(t, mux, "/api/shifts/adjustment", body); rec.Code != http.StatusConflict {
		t.Fatalf("expected the pre-override payout to be refused, got %d: %s", rec.Code, rec.Body.String())
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

	rec := adjustmentRequest(t, mux, "/api/shifts/adjustment", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 under an active override, got %d: %s", rec.Code, rec.Body.String())
	}
	adjData, hasData, _, _ := envelopeOf(t, rec.Body.Bytes())
	if !hasData {
		t.Fatalf("expected a data envelope, got %s", rec.Body.String())
	}
	var resp CashAdjustmentResponse
	if err := json.Unmarshal(adjData, &resp); err != nil {
		t.Fatal(err)
	}

	var entityID, payload string
	if err := dp.Db.QueryRow(`SELECT entity_id, data_json FROM audit_log WHERE entity_type='shift_adjustment' AND action='unsigned_override'`).
		Scan(&entityID, &payload); err != nil {
		t.Fatalf("expected a shift_adjustment/unsigned_override audit row: %v", err)
	}
	if entityID != resp.AdjustmentID {
		t.Fatalf("unsigned_override marker not attached to the adjustment: %q != %q", entityID, resp.AdjustmentID)
	}
	if !strings.Contains(payload, "user1") {
		t.Fatalf("per-adjustment marker missing actor: %s", payload)
	}
}

// PfandRueckgabe is always a payout — the gate applies unconditionally,
// with no sign check to bypass.
func TestFiscalGate_PfandRueckgabeBlockedWhenTSENeverConfigured(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newShiftsFiscalTestDeps(t)
	makeGermanSystemOfRecord(t, dp)
	openShiftForFiscalGateTest(t, mux, dp)

	rec := adjustmentRequest(t, mux, "/api/shifts/pfandrueckgabe", `{"amount":500}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "technical security device") {
		t.Fatalf("expected the localized refund.error.fiscal_never_configured copy, got: %s", rec.Body.String())
	}
	if got := countShiftAdjustments(t, dp); got != 0 {
		t.Fatalf("expected no cash_adjustment audit row, got %d", got)
	}
}

func TestFiscalGate_PfandRueckgabeOverrideUnblocksAndAudits(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newShiftsFiscalTestDeps(t)
	seedFailingConfiguredTSE(t, dp)
	openShiftForFiscalGateTest(t, mux, dp)

	if rec := adjustmentRequest(t, mux, "/api/shifts/pfandrueckgabe", `{"amount":500}`); rec.Code != http.StatusConflict {
		t.Fatalf("expected the pre-override payout to be refused, got %d: %s", rec.Code, rec.Body.String())
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

	rec := adjustmentRequest(t, mux, "/api/shifts/pfandrueckgabe", `{"amount":500}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 under an active override, got %d: %s", rec.Code, rec.Body.String())
	}
	adjData, hasData, _, _ := envelopeOf(t, rec.Body.Bytes())
	if !hasData {
		t.Fatalf("expected a data envelope, got %s", rec.Body.String())
	}
	var resp CashAdjustmentResponse
	if err := json.Unmarshal(adjData, &resp); err != nil {
		t.Fatal(err)
	}
	var entityID string
	if err := dp.Db.QueryRow(`SELECT entity_id FROM audit_log WHERE entity_type='shift_adjustment' AND action='unsigned_override'`).
		Scan(&entityID); err != nil {
		t.Fatalf("expected a shift_adjustment/unsigned_override audit row: %v", err)
	}
	if entityID != resp.AdjustmentID {
		t.Fatalf("unsigned_override marker not attached to the payout: %q != %q", entityID, resp.AdjustmentID)
	}
}

// The refusal copy must come from the locale files, not raw Go/SQL error
// text — mirrors fiscal_gate_test.go's own TestFiscalGate_RefusalIsTranslated.
func TestFiscalGate_CashAdjustmentRefusalIsTranslated(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newShiftsFiscalTestDeps(t)
	makeGermanSystemOfRecord(t, dp)
	shiftID := openShiftForFiscalGateTest(t, mux, dp)

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

	req := httptest.NewRequest(http.MethodPost, "/api/shifts/adjustment?lang=fa",
		strings.NewReader(`{"shift_id":"`+shiftID+`","type":"payout","amount":-500,"reason":"float top-up to safe"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req = auth.WithUser(req, auth.User{ID: "user1"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), want) {
		t.Fatalf("expected fa translation %q in refusal, got: %s", want, rec.Body.String())
	}
}
